package resourcecenter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"ops-deploy-platform/internal/environment"
)

const cloudConfigurationSchemaVersion = 5

type actualCloudResource struct {
	Key    string
	Exists bool
	Status string
	Fields map[string]any
	Error  error
}

type cloudFieldSpec struct {
	Path     string
	Label    string
	Syncable bool
}

// RefreshCloudConfiguration performs only the AWS configuration read. It is
// used by the deployment preflight so a drift check does not also wait for
// Kubernetes inventory and Terraform output collection.
func (s *Service) RefreshCloudConfiguration(ctx context.Context, project, environmentName, targetName string) (Snapshot, error) {
	doc, err := s.environments.Load(targetName)
	if err != nil {
		return Snapshot{}, err
	}
	doc = environment.ApplyDefaults(doc, project, environmentName)
	previous, _ := s.Load(ctx, project, environmentName)
	snapshot := previous
	if snapshot.Resources == nil {
		snapshot.Resources = make([]Resource, 0)
	}
	snapshot.SchemaVersion = cloudConfigurationSchemaVersion
	snapshot.Project = project
	snapshot.Environment = environmentName
	snapshot.ObservedAt = time.Now().UTC()
	actual, warnings := s.collectCloudConfiguration(ctx, project, doc, previous)
	s.attachCloudConfiguration(&snapshot, doc, actual, previous)
	// Preserve unrelated collection warnings but replace transient AWS-read
	// warnings with the newest result.
	filtered := make([]string, 0, len(snapshot.Warnings)+len(warnings))
	for _, warning := range snapshot.Warnings {
		if !strings.Contains(warning, "实际参数读取失败") {
			filtered = append(filtered, warning)
		}
	}
	snapshot.Warnings = append(filtered, warnings...)
	if err := s.persistSnapshot(ctx, snapshot); err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

// ResetMissingCloudConfigurationAfterDestroy removes only stale configuration
// baselines for AWS resources that are already absent. A successful platform
// destroy is an explicit lifecycle boundary: the next phase-one deployment is
// a create, not an attempt to reconcile the old cluster generation. Resources
// that still exist (for example shared/reused ECR repositories) are preserved
// and continue to participate in the normal three-way drift comparison.
func (s *Service) ResetMissingCloudConfigurationAfterDestroy(ctx context.Context, project, environmentName string) error {
	snapshot, err := s.Load(ctx, project, environmentName)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	resources := make([]Resource, 0, len(snapshot.Resources))
	changed := false
	for _, resource := range snapshot.Resources {
		awsStatus := strings.TrimSpace(stringFrom(resource.Metadata["aws_status"]))
		if cloudConfigurationResourceKey(resource.Key) &&
			(strings.EqualFold(awsStatus, "NOT_FOUND") || strings.EqualFold(resource.Status, "missing")) {
			changed = true
			continue
		}
		resources = append(resources, resource)
	}
	if !changed {
		return nil
	}
	snapshot.Resources = resources
	snapshot.CloudSync = summarizeCloudFields(resources, time.Now().UTC())
	return s.persistSnapshot(ctx, snapshot)
}

func (s *Service) collectCloudConfiguration(ctx context.Context, project string, doc environment.Document, previous Snapshot) (map[string]actualCloudResource, []string) {
	region := stringPath(doc, "region")
	prefix := stringPath(doc, "project") + "-" + stringPath(doc, "environment")
	tasks := []func(context.Context) actualCloudResource{
		func(taskCtx context.Context) actualCloudResource { return s.inspectEKS(taskCtx, project, region, doc) },
	}
	services := mapPath(doc, "data_services")
	if boolFrom(mapFrom(services["rds"])["enabled"]) || snapshotHasCloudResource(previous, "rds") {
		tasks = append(tasks, func(taskCtx context.Context) actualCloudResource {
			return s.inspectRDSInstance(taskCtx, project, region, "rds", prefix+"-admin")
		})
	}
	if boolFrom(mapFrom(services["postgres"])["enabled"]) || snapshotHasCloudResource(previous, "postgres") {
		tasks = append(tasks, func(taskCtx context.Context) actualCloudResource {
			return s.inspectRDSInstance(taskCtx, project, region, "postgres", prefix+"-postgres")
		})
	}
	if boolFrom(mapFrom(services["aurora"])["enabled"]) || snapshotHasCloudResource(previous, "aurora") {
		tasks = append(tasks, func(taskCtx context.Context) actualCloudResource {
			return s.inspectAurora(taskCtx, project, region, prefix+"-game")
		})
	}
	if boolFrom(mapFrom(services["documentdb"])["enabled"]) || snapshotHasCloudResource(previous, "documentdb") {
		tasks = append(tasks, func(taskCtx context.Context) actualCloudResource {
			return s.inspectDocumentDB(taskCtx, project, region, prefix+"-documentdb")
		})
	}
	if elasticache := mapFrom(services["elasticache"]); boolFrom(elasticache["enabled"]) || snapshotHasCloudResource(previous, "elasticache") {
		mode := defaultString(stringFrom(elasticache["mode"]), "cluster")
		tasks = append(tasks, func(taskCtx context.Context) actualCloudResource {
			return s.inspectElastiCache(taskCtx, project, region, prefix+"-game", mode)
		})
	}
	if boolFrom(mapFrom(services["msk"])["enabled"]) || snapshotHasCloudResource(previous, "msk") {
		tasks = append(tasks, func(taskCtx context.Context) actualCloudResource {
			return s.inspectMSK(taskCtx, project, region, prefix+"-kafka")
		})
	}
	if boolFrom(mapFrom(services["amazon_mq"])["enabled"]) || snapshotHasCloudResource(previous, "amazon_mq") {
		tasks = append(tasks, func(taskCtx context.Context) actualCloudResource {
			return s.inspectAmazonMQ(taskCtx, project, region, prefix+"-rabbitmq")
		})
	}
	if boolFrom(mapPath(doc, "ecr")["enabled"]) || snapshotHasCloudResource(previous, "ecr") {
		tasks = append(tasks, func(taskCtx context.Context) actualCloudResource {
			return s.inspectECR(taskCtx, project, region, doc)
		})
	}

	results := make(chan actualCloudResource, len(tasks))
	semaphore := make(chan struct{}, 4)
	var wait sync.WaitGroup
	for _, task := range tasks {
		wait.Add(1)
		go func(run func(context.Context) actualCloudResource) {
			defer wait.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()
			taskCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
			defer cancel()
			results <- run(taskCtx)
		}(task)
	}
	wait.Wait()
	close(results)

	actual := make(map[string]actualCloudResource, len(tasks))
	warnings := make([]string, 0)
	for result := range results {
		actual[result.Key] = result
		if result.Error != nil {
			warnings = append(warnings, fmt.Sprintf("%s 实际参数读取失败：%v", cloudResourceDisplayName(result.Key), result.Error))
		}
	}
	sort.Strings(warnings)
	return actual, warnings
}

func (s *Service) inspectEKS(ctx context.Context, project, region string, doc environment.Document) actualCloudResource {
	clusterName := environment.ClusterName(doc)
	if environment.IsExistingEKS(doc) {
		clusterName = stringPath(doc, "deployment_target.cluster_name")
	}
	result := actualCloudResource{Key: "eks", Fields: make(map[string]any)}
	root, err := s.awsObject(ctx, project, region, "eks", "describe-cluster", "--name", clusterName)
	if err != nil {
		result.Error = normalizeAWSReadError(err)
		return result
	}
	cluster := objectMap(root["cluster"])
	if len(cluster) == 0 {
		return result
	}
	result.Exists, result.Status = true, stringFrom(cluster["status"])
	result.Fields["eks.kubernetes_version"] = cluster["version"]
	vpc := objectMap(cluster["resourcesVpcConfig"])
	result.Fields["eks.endpoint_private_access"] = vpc["endpointPrivateAccess"]
	result.Fields["eks.endpoint_public_access"] = vpc["endpointPublicAccess"]
	result.Fields["eks.public_access_cidrs"] = sortedStrings(vpc["publicAccessCidrs"])
	if environment.IsExistingEKS(doc) {
		return result
	}
	for groupName := range mapPath(doc, "eks.node_groups") {
		groupRoot, groupErr := s.awsObject(ctx, project, region, "eks", "describe-nodegroup", "--cluster-name", clusterName, "--nodegroup-name", groupName)
		if groupErr != nil {
			if !isAWSNotFound(groupErr) {
				result.Error = errors.Join(result.Error, fmt.Errorf("节点组 %s: %w", groupName, groupErr))
			}
			continue
		}
		group := objectMap(groupRoot["nodegroup"])
		base := "eks.node_groups." + groupName + "."
		result.Fields[base+"instance_types"] = sortedStrings(group["instanceTypes"])
		result.Fields[base+"capacity_type"] = group["capacityType"]
		result.Fields[base+"ami_type"] = group["amiType"]
		scaling := objectMap(group["scalingConfig"])
		result.Fields[base+"min_size"] = scaling["minSize"]
		// desiredSize is deliberately observation-only. Cluster Autoscaler owns
		// it at runtime and Terraform already ignores that attribute.
		result.Fields[base+"desired_size"] = scaling["desiredSize"]
		result.Fields[base+"max_size"] = scaling["maxSize"]
		if diskSize := group["diskSize"]; diskSize != nil {
			result.Fields[base+"disk_size"] = diskSize
		} else if launchTemplate := objectMap(group["launchTemplate"]); len(launchTemplate) > 0 {
			args := []string{"ec2", "describe-launch-template-versions"}
			if identifier := stringFrom(launchTemplate["id"]); identifier != "" {
				args = append(args, "--launch-template-id", identifier)
			} else if name := stringFrom(launchTemplate["name"]); name != "" {
				args = append(args, "--launch-template-name", name)
			}
			if version := stringFrom(launchTemplate["version"]); version != "" {
				args = append(args, "--versions", version)
			}
			if len(args) > 2 {
				launchRoot, launchErr := s.awsObject(ctx, project, region, args...)
				if launchErr != nil {
					result.Error = errors.Join(result.Error, fmt.Errorf("节点组 %s 启动模板: %w", groupName, launchErr))
				} else if versions := objectSlice(launchRoot["LaunchTemplateVersions"]); len(versions) > 0 {
					mappings := objectSlice(valueAt(versions[0], "LaunchTemplateData.BlockDeviceMappings"))
					if len(mappings) > 0 {
						result.Fields[base+"disk_size"] = valueAt(mappings[0], "Ebs.VolumeSize")
					}
				}
			}
		}
	}
	return result
}

func (s *Service) inspectRDSInstance(ctx context.Context, project, region, key, identifier string) actualCloudResource {
	result := actualCloudResource{Key: key, Fields: make(map[string]any)}
	root, err := s.awsObject(ctx, project, region, "rds", "describe-db-instances", "--db-instance-identifier", identifier)
	if err != nil {
		result.Error = normalizeAWSReadError(err)
		return result
	}
	items := objectSlice(root["DBInstances"])
	if len(items) == 0 {
		return result
	}
	item := items[0]
	result.Exists, result.Status = true, stringFrom(item["DBInstanceStatus"])
	base := "data_services." + key + "."
	result.Fields[base+"enabled"] = true
	copyFields(result.Fields, base, item, map[string]string{
		"engine": "Engine", "engine_version": "EngineVersion", "instance_class": "DBInstanceClass",
		"database_name": "DBName", "master_username": "MasterUsername", "port": "Endpoint.Port",
		"allocated_storage": "AllocatedStorage", "max_allocated_storage": "MaxAllocatedStorage", "multi_az": "MultiAZ",
		"backup_retention_days": "BackupRetentionPeriod", "backup_window": "PreferredBackupWindow",
		"maintenance_window": "PreferredMaintenanceWindow", "auto_minor_version_upgrade": "AutoMinorVersionUpgrade",
		"performance_insights_enabled": "PerformanceInsightsEnabled", "deletion_protection": "DeletionProtection",
	})
	return result
}

func (s *Service) inspectAurora(ctx context.Context, project, region, identifier string) actualCloudResource {
	result := actualCloudResource{Key: "aurora", Fields: make(map[string]any)}
	root, err := s.awsObject(ctx, project, region, "rds", "describe-db-clusters", "--db-cluster-identifier", identifier)
	if err != nil {
		result.Error = normalizeAWSReadError(err)
		return result
	}
	items := objectSlice(root["DBClusters"])
	if len(items) == 0 {
		return result
	}
	item := items[0]
	result.Exists, result.Status = true, stringFrom(item["Status"])
	base := "data_services.aurora."
	result.Fields[base+"enabled"] = true
	copyFields(result.Fields, base, item, map[string]string{
		"engine": "Engine", "engine_version": "EngineVersion", "database_name": "DatabaseName",
		"master_username": "MasterUsername", "port": "Port", "backup_retention_days": "BackupRetentionPeriod",
		"backup_window": "PreferredBackupWindow", "maintenance_window": "PreferredMaintenanceWindow",
		"deletion_protection": "DeletionProtection",
	})
	backtrackSeconds := intFrom(item["BacktrackWindow"])
	result.Fields[base+"backtrack_enabled"] = backtrackSeconds > 0
	if backtrackSeconds > 0 {
		result.Fields[base+"backtrack_window_hours"] = backtrackSeconds / 3600
	}
	members := objectSlice(item["DBClusterMembers"])
	result.Fields[base+"instance_count"] = len(members)
	for _, member := range members {
		instanceID := stringFrom(member["DBInstanceIdentifier"])
		if instanceID == "" {
			continue
		}
		instanceRoot, instanceErr := s.awsObject(ctx, project, region, "rds", "describe-db-instances", "--db-instance-identifier", instanceID)
		if instanceErr != nil {
			result.Error = errors.Join(result.Error, fmt.Errorf("Aurora 实例 %s: %w", instanceID, instanceErr))
			continue
		}
		instances := objectSlice(instanceRoot["DBInstances"])
		if len(instances) == 0 {
			continue
		}
		for field, source := range map[string]string{
			"auto_minor_version_upgrade":   "AutoMinorVersionUpgrade",
			"performance_insights_enabled": "PerformanceInsightsEnabled",
		} {
			if uniformErr := setUniformCloudField(result.Fields, base+field, instances[0][source]); uniformErr != nil {
				result.Error = errors.Join(result.Error, fmt.Errorf("Aurora 各实例 %s 不一致: %w", field, uniformErr))
			}
		}
	}
	scaling := objectMap(item["ServerlessV2ScalingConfiguration"])
	if len(scaling) > 0 {
		result.Fields[base+"min_acu"] = scaling["MinCapacity"]
		result.Fields[base+"max_acu"] = scaling["MaxCapacity"]
	}
	return result
}

func (s *Service) inspectDocumentDB(ctx context.Context, project, region, identifier string) actualCloudResource {
	result := actualCloudResource{Key: "documentdb", Fields: make(map[string]any)}
	root, err := s.awsObject(ctx, project, region, "docdb", "describe-db-clusters", "--db-cluster-identifier", identifier)
	if err != nil {
		result.Error = normalizeAWSReadError(err)
		return result
	}
	items := objectSlice(root["DBClusters"])
	if len(items) == 0 {
		return result
	}
	item := items[0]
	result.Exists, result.Status = true, stringFrom(item["Status"])
	base := "data_services.documentdb."
	result.Fields[base+"enabled"] = true
	copyFields(result.Fields, base, item, map[string]string{
		"engine": "Engine", "engine_version": "EngineVersion", "master_username": "MasterUsername",
		"port": "Port", "storage_type": "StorageType", "backup_retention_days": "BackupRetentionPeriod", "deletion_protection": "DeletionProtection",
	})
	members := objectSlice(item["DBClusterMembers"])
	result.Fields[base+"instance_count"] = len(members)
	for _, member := range members {
		instanceID := stringFrom(member["DBInstanceIdentifier"])
		if instanceID != "" {
			instanceRoot, instanceErr := s.awsObject(ctx, project, region, "docdb", "describe-db-instances", "--db-instance-identifier", instanceID)
			if instanceErr == nil {
				instances := objectSlice(instanceRoot["DBInstances"])
				if len(instances) > 0 {
					if uniformErr := setUniformCloudField(result.Fields, base+"instance_class", instances[0]["DBInstanceClass"]); uniformErr != nil {
						result.Error = errors.Join(result.Error, fmt.Errorf("DocumentDB 各实例规格不一致: %w", uniformErr))
					}
					if uniformErr := setUniformCloudField(result.Fields, base+"auto_minor_version_upgrade", instances[0]["AutoMinorVersionUpgrade"]); uniformErr != nil {
						result.Error = errors.Join(result.Error, fmt.Errorf("DocumentDB 各实例自动升级设置不一致: %w", uniformErr))
					}
				}
			} else {
				result.Error = errors.Join(result.Error, fmt.Errorf("DocumentDB 实例 %s: %w", instanceID, instanceErr))
			}
		}
	}
	return result
}

func (s *Service) inspectElastiCache(ctx context.Context, project, region, name, mode string) actualCloudResource {
	result := actualCloudResource{Key: "elasticache", Fields: make(map[string]any)}
	base := "data_services.elasticache."
	result.Fields[base+"mode"] = mode
	if mode == "serverless" {
		root, err := s.awsObject(ctx, project, region, "elasticache", "describe-serverless-caches", "--serverless-cache-name", name)
		if err != nil {
			result.Error = normalizeAWSReadError(err)
			return result
		}
		items := objectSlice(root["ServerlessCaches"])
		if len(items) == 0 {
			return result
		}
		item := items[0]
		result.Exists, result.Status = true, stringFrom(item["Status"])
		result.Fields[base+"enabled"] = true
		result.Fields[base+"engine"] = strings.ToLower(stringFrom(item["Engine"]))
		result.Fields[base+"engine_version"] = firstNonEmpty(item["FullEngineVersion"], item["MajorEngineVersion"])
		result.Fields[base+"snapshot_retention_days"] = item["SnapshotRetentionLimit"]
		limits := objectMap(item["CacheUsageLimits"])
		storage := objectMap(limits["DataStorage"])
		ecpu := objectMap(limits["ECPUPerSecond"])
		result.Fields[base+"max_storage_gb"] = storage["Maximum"]
		result.Fields[base+"max_ecpu"] = ecpu["Maximum"]
		return result
	}
	root, err := s.awsObject(ctx, project, region, "elasticache", "describe-replication-groups", "--replication-group-id", name)
	if err != nil {
		result.Error = normalizeAWSReadError(err)
		return result
	}
	items := objectSlice(root["ReplicationGroups"])
	if len(items) == 0 {
		return result
	}
	item := items[0]
	result.Exists, result.Status = true, stringFrom(item["Status"])
	result.Fields[base+"enabled"] = true
	nodeGroups := objectSlice(item["NodeGroups"])
	result.Fields[base+"num_node_groups"] = len(nodeGroups)
	nodesPerShard := 0
	for _, group := range nodeGroups {
		if count := len(objectSlice(group["NodeGroupMembers"])); count > nodesPerShard {
			nodesPerShard = count
		}
	}
	if nodesPerShard > 0 {
		result.Fields[base+"nodes_per_shard"] = nodesPerShard
		result.Fields[base+"replicas_per_node_group"] = nodesPerShard - 1
	}
	copyFields(result.Fields, base, item, map[string]string{
		"node_type": "CacheNodeType", "snapshot_retention_days": "SnapshotRetentionLimit",
		"snapshot_window": "SnapshotWindow", "maintenance_window": "PreferredMaintenanceWindow",
		"tls_enabled": "TransitEncryptionEnabled",
	})
	memberIDs := stringSlice(item["MemberClusters"])
	if len(memberIDs) > 0 {
		clusterRoot, clusterErr := s.awsObject(ctx, project, region, "elasticache", "describe-cache-clusters", "--cache-cluster-id", memberIDs[0], "--show-cache-node-info")
		if clusterErr == nil {
			clusters := objectSlice(clusterRoot["CacheClusters"])
			if len(clusters) > 0 {
				cluster := clusters[0]
				result.Fields[base+"engine"] = strings.ToLower(stringFrom(cluster["Engine"]))
				result.Fields[base+"engine_version"] = cluster["EngineVersion"]
				result.Fields[base+"node_type"] = cluster["CacheNodeType"]
				result.Fields[base+"auto_minor_version_upgrade"] = cluster["AutoMinorVersionUpgrade"]
				parameterGroup := objectMap(cluster["CacheParameterGroup"])
				result.Fields[base+"parameter_group_name"] = parameterGroup["CacheParameterGroupName"]
				endpoint := objectMap(cluster["ConfigurationEndpoint"])
				if len(endpoint) == 0 {
					endpoint = objectMap(cluster["CacheNodes"])
				}
				_ = endpoint
			}
		}
	}
	return result
}

func (s *Service) inspectMSK(ctx context.Context, project, region, name string) actualCloudResource {
	result := actualCloudResource{Key: "msk", Fields: make(map[string]any)}
	root, err := s.awsObject(ctx, project, region, "kafka", "list-clusters-v2", "--cluster-name-filter", name)
	if err != nil {
		result.Error = err
		return result
	}
	items := objectSlice(root["ClusterInfoList"])
	var item map[string]any
	for _, candidate := range items {
		if stringFrom(candidate["ClusterName"]) == name {
			item = candidate
			break
		}
	}
	if len(item) == 0 {
		return result
	}
	result.Exists, result.Status = true, stringFrom(item["State"])
	base := "data_services.msk."
	result.Fields[base+"enabled"] = true
	if serverless := objectMap(item["Serverless"]); len(serverless) > 0 {
		result.Fields[base+"mode"] = "serverless"
		return result
	}
	result.Fields[base+"mode"] = "provisioned"
	provisioned := objectMap(item["Provisioned"])
	result.Fields[base+"kafka_version"] = valueAt(provisioned, "CurrentBrokerSoftwareInfo.KafkaVersion")
	result.Fields[base+"instance_type"] = valueAt(provisioned, "BrokerNodeGroupInfo.InstanceType")
	result.Fields[base+"broker_count"] = provisioned["NumberOfBrokerNodes"]
	result.Fields[base+"volume_size"] = valueAt(provisioned, "BrokerNodeGroupInfo.StorageInfo.EbsStorageInfo.VolumeSize")
	result.Fields[base+"enhanced_monitoring"] = provisioned["EnhancedMonitoring"]
	return result
}

func (s *Service) inspectAmazonMQ(ctx context.Context, project, region, name string) actualCloudResource {
	result := actualCloudResource{Key: "amazon_mq", Fields: make(map[string]any)}
	root, err := s.awsObject(ctx, project, region, "mq", "list-brokers")
	if err != nil {
		result.Error = err
		return result
	}
	brokerID := ""
	for _, summary := range objectSlice(root["BrokerSummaries"]) {
		if stringFrom(summary["BrokerName"]) == name {
			brokerID = stringFrom(summary["BrokerId"])
			result.Status = stringFrom(summary["BrokerState"])
			break
		}
	}
	if brokerID == "" {
		return result
	}
	detail, detailErr := s.awsObject(ctx, project, region, "mq", "describe-broker", "--broker-id", brokerID)
	if detailErr != nil {
		result.Error = detailErr
		return result
	}
	result.Exists = true
	result.Status = defaultString(stringFrom(detail["BrokerState"]), result.Status)
	base := "data_services.amazon_mq."
	result.Fields[base+"enabled"] = true
	engine := stringFrom(valueAt(detail, "EngineType"))
	if strings.EqualFold(engine, "RABBITMQ") {
		engine = "RabbitMQ"
	}
	result.Fields[base+"engine"] = engine
	result.Fields[base+"engine_version"] = valueAt(detail, "EngineVersion")
	result.Fields[base+"deployment_mode"] = valueAt(detail, "DeploymentMode")
	result.Fields[base+"host_instance_type"] = valueAt(detail, "HostInstanceType")
	result.Fields[base+"auto_minor_version_upgrade"] = valueAt(detail, "AutoMinorVersionUpgrade")
	result.Fields[base+"general_logs_enabled"] = valueAt(detail, "Logs.General")
	return result
}

func (s *Service) inspectECR(ctx context.Context, project, region string, doc environment.Document) actualCloudResource {
	result := actualCloudResource{Key: "ecr", Fields: make(map[string]any)}
	root, err := s.awsObject(ctx, project, region, "ecr", "describe-repositories")
	if err != nil {
		result.Error = normalizeAWSReadError(err)
		return result
	}
	prefix := strings.Trim(strings.TrimSpace(project), "/") + "/"
	configured := configuredECRRepositories(project, doc)
	items := make([]map[string]any, 0)
	configuredItems := make([]map[string]any, 0)
	for _, item := range objectSlice(root["repositories"]) {
		name := stringFrom(item["repositoryName"])
		if strings.HasPrefix(name, prefix) {
			items = append(items, item)
		}
		if _, managedByEnvironment := configured[name]; managedByEnvironment {
			configuredItems = append(configuredItems, item)
		}
	}
	actualNames := make([]string, 0, len(items))
	for _, item := range items {
		actualNames = append(actualNames, strings.TrimPrefix(stringFrom(item["repositoryName"]), prefix))
	}
	sort.Strings(actualNames)
	result.Exists, result.Status = len(items) > 0, "ACTIVE"
	result.Fields["ecr.enabled"] = len(items) > 0
	result.Fields["ecr.repositories"] = actualNames
	if len(configuredItems) > 0 {
		mutability := canonicalCloudValue(configuredItems[0]["imageTagMutability"])
		scanOnPush := canonicalCloudValue(valueAt(configuredItems[0], "imageScanningConfiguration.scanOnPush"))
		for _, item := range configuredItems[1:] {
			name := stringFrom(item["repositoryName"])
			if !cloudValuesEqual(mutability, item["imageTagMutability"]) || !cloudValuesEqual(scanOnPush, valueAt(item, "imageScanningConfiguration.scanOnPush")) {
				result.Error = fmt.Errorf("项目 ECR 仓库设置不一致（%s）；请先统一 Tag 可变性和推送扫描策略", name)
				return result
			}
		}
		result.Fields["ecr.image_tag_mutability"] = mutability
		result.Fields["ecr.scan_on_push"] = scanOnPush
	}
	return result
}

func configuredECRRepositories(project string, doc environment.Document) map[string]struct{} {
	prefix := strings.Trim(strings.TrimSpace(project), "/") + "/"
	configured := make(map[string]struct{})
	for _, value := range stringSlicePath(doc, "ecr.repositories") {
		name := strings.Trim(strings.TrimSpace(value), "/")
		if name == "" {
			continue
		}
		if !strings.HasPrefix(name, prefix) {
			name = prefix + name
		}
		configured[name] = struct{}{}
	}
	return configured
}

func (s *Service) attachCloudConfiguration(snapshot *Snapshot, doc environment.Document, actual map[string]actualCloudResource, previous Snapshot) {
	previousByKey := make(map[string]Resource, len(previous.Resources))
	for _, resource := range previous.Resources {
		previousByKey[resource.Key] = resource
	}
	resourceByKey := make(map[string]int, len(snapshot.Resources))
	for index := range snapshot.Resources {
		resourceByKey[snapshot.Resources[index].Key] = index
	}
	keys := make([]string, 0, len(actual))
	for key := range actual {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	summary := CloudSync{Status: "synced", ObservedAt: snapshot.ObservedAt}
	for _, key := range keys {
		observed := actual[key]
		if observed.Error != nil {
			summary.Unavailable++
			continue
		}
		if !observed.Exists {
			prior, deployedBefore := previousByKey[key]
			if !deployedBefore || (len(prior.Configuration) == 0 && len(prior.Baseline) == 0 && len(prior.AccessPoints) == 0) {
				continue
			}
			// A resource that the platform deliberately disabled and AWS has
			// already removed is converged. Keep no stale drift marker that would
			// block every later phase-one deployment forever.
			if !cloudResourceDesiredEnabled(key, doc) {
				if resourceIndex, found := resourceByKey[key]; found {
					if snapshot.Resources[resourceIndex].Metadata == nil {
						snapshot.Resources[resourceIndex].Metadata = make(map[string]any)
					}
					snapshot.Resources[resourceIndex].Configuration = nil
					snapshot.Resources[resourceIndex].Baseline = nil
					snapshot.Resources[resourceIndex].Status = "missing"
					snapshot.Resources[resourceIndex].Metadata["aws_status"] = "NOT_FOUND"
				}
				continue
			}
			resourceIndex, found := resourceByKey[key]
			if !found {
				copy := prior
				snapshot.Resources = append(snapshot.Resources, copy)
				resourceIndex = len(snapshot.Resources) - 1
				resourceByKey[key] = resourceIndex
			}
			resource := &snapshot.Resources[resourceIndex]
			if resource.Metadata == nil {
				resource.Metadata = make(map[string]any)
			}
			resource.Metadata["aws_status"] = "NOT_FOUND"
			resource.Status = "missing"
			resource.Configuration = make([]ConfigurationField, 0)
			resource.Baseline = cloneAnyMap(prior.Baseline)
			for _, spec := range cloudFieldSpecs(key, doc) {
				desired, _ := environment.GetPath(doc, spec.Path)
				resource.Configuration = append(resource.Configuration, ConfigurationField{
					Path: spec.Path, Label: spec.Label, Desired: canonicalCloudValue(desired),
					State: "drifted", Syncable: false,
				})
				summary.DriftedFields++
			}
			continue
		}
		resourceIndex, found := resourceByKey[key]
		if !found {
			snapshot.Resources = append(snapshot.Resources, Resource{
				Key: key, DisplayName: cloudResourceDisplayName(key), Category: cloudResourceCategory(key),
				Source: "cloud", Provider: "AWS", Status: cloudRuntimeStatus(observed.Status),
				AccessPoints: []AccessPoint{}, Credentials: []Credential{}, Metadata: map[string]any{},
			})
			resourceIndex = len(snapshot.Resources) - 1
			resourceByKey[key] = resourceIndex
		}
		resource := &snapshot.Resources[resourceIndex]
		if resource.Metadata == nil {
			resource.Metadata = make(map[string]any)
		}
		resource.Metadata["aws_status"] = observed.Status
		resource.Status = cloudRuntimeStatus(observed.Status)
		prior := previousByKey[key]
		baseline := cloneAnyMap(prior.Baseline)
		if baseline == nil {
			baseline = make(map[string]any)
		}
		fields := make([]ConfigurationField, 0)
		for _, spec := range cloudFieldSpecs(key, doc) {
			actualValue, found := observed.Fields[spec.Path]
			if !found || actualValue == nil {
				continue
			}
			desiredValue, _ := environment.GetPath(doc, spec.Path)
			actualValue = canonicalCloudValue(actualValue)
			desiredValue = canonicalCloudValue(desiredValue)
			baselineValue, hasBaseline := baseline[spec.Path]
			if !hasBaseline {
				// Existing schema-v3 snapshots contain no baseline. Treat the
				// platform's saved value as the last managed value so a console
				// edit is not silently accepted during migration.
				baselineValue = desiredValue
				baseline[spec.Path] = desiredValue
			}
			state := cloudFieldState(desiredValue, actualValue, baselineValue)
			if additiveCloudFieldPath(spec.Path) {
				state = additiveStringSetState(desiredValue, actualValue)
			}
			if (key == "eks" && environment.IsExistingEKS(doc)) || strings.HasSuffix(spec.Path, ".desired_size") {
				state = "observed"
			}
			if state == "synced" {
				baseline[spec.Path] = actualValue
			}
			fields = append(fields, ConfigurationField{
				Path: spec.Path, Label: spec.Label, Desired: desiredValue, Actual: actualValue,
				State: state, Syncable: spec.Syncable,
			})
			switch state {
			case "synced":
				summary.SyncedFields++
			case "pending":
				summary.PendingFields++
			case "drifted":
				summary.DriftedFields++
			case "conflict":
				summary.ConflictFields++
			}
		}
		resource.Configuration = fields
		resource.Baseline = baseline
	}
	summary.BlockingChanges = summary.DriftedFields > 0 || summary.ConflictFields > 0
	switch {
	case summary.ConflictFields > 0:
		summary.Status = "conflict"
	case summary.DriftedFields > 0:
		summary.Status = "drifted"
	case summary.PendingFields > 0:
		summary.Status = "pending"
	case summary.Unavailable > 0:
		summary.Status = "unavailable"
	}
	snapshot.CloudSync = summary
}

func (s *Service) SyncDesiredFromAWS(ctx context.Context, project, environmentName, targetName string) (environment.Document, Snapshot, error) {
	snapshot, err := s.RefreshCloudConfiguration(ctx, project, environmentName, targetName)
	if err != nil {
		return nil, Snapshot{}, err
	}
	if snapshot.CloudSync.Unavailable > 0 {
		details := make([]string, 0)
		for _, warning := range snapshot.Warnings {
			if strings.Contains(warning, "实际参数读取失败") {
				details = append(details, warning)
			}
		}
		if len(details) > 0 {
			return nil, snapshot, fmt.Errorf("部分 AWS 资源实际参数读取失败，平台拒绝用不完整快照覆盖部署配置：%s", strings.Join(details, "；"))
		}
		return nil, snapshot, errors.New("部分 AWS 资源实际参数读取失败，平台拒绝用不完整快照覆盖部署配置")
	}
	doc, err := s.environments.Load(targetName)
	if err != nil {
		return nil, snapshot, err
	}
	changed := 0
	for index := range snapshot.Resources {
		resource := &snapshot.Resources[index]
		if len(resource.Configuration) == 0 {
			continue
		}
		if !cloudResourceStable(stringFrom(resource.Metadata["aws_status"])) {
			return nil, snapshot, fmt.Errorf("%s 当前状态为 %s，等待 AWS 操作完成后再同步参数", resource.DisplayName, resource.Metadata["aws_status"])
		}
		for fieldIndex := range resource.Configuration {
			field := &resource.Configuration[fieldIndex]
			if !field.Syncable || field.State == "synced" || field.Actual == nil {
				continue
			}
			documentValue := cloudDocumentValue(field.Actual)
			environment.SetPath(doc, field.Path, documentValue)
			field.Desired = documentValue
			field.State = "synced"
			resource.Baseline[field.Path] = documentValue
			changed++
		}
	}
	doc = environment.ApplyDefaults(doc, project, environmentName)
	if err := s.environments.Save(targetName, doc); err != nil {
		return nil, snapshot, fmt.Errorf("保存 AWS 实际参数到平台配置: %w", err)
	}
	snapshot.CloudSync = summarizeCloudFields(snapshot.Resources, snapshot.ObservedAt)
	if changed > 0 {
		snapshot.Warnings = append(snapshot.Warnings, fmt.Sprintf("已采用 AWS 实际值同步 %d 个配置字段；未修改 AWS 资源", changed))
	}
	if err := s.persistSnapshot(ctx, snapshot); err != nil {
		return nil, Snapshot{}, err
	}
	return doc, snapshot, nil
}

// cloudDocumentValue converts typed Go slices and maps returned by AWS
// inspection helpers into the JSON-compatible []any/map[string]any shape used
// by environment documents.
func cloudDocumentValue(value any) any {
	payload, err := json.Marshal(value)
	if err != nil {
		return value
	}
	var normalized any
	if err := json.Unmarshal(payload, &normalized); err != nil {
		return value
	}
	return normalized
}

func (s *Service) persistSnapshot(ctx context.Context, snapshot Snapshot) error {
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	if s.store != nil {
		return s.store.SaveResourceSnapshot(ctx, snapshot.Project, snapshot.Environment, payload)
	}
	return nil
}

func summarizeCloudFields(resources []Resource, observedAt time.Time) CloudSync {
	summary := CloudSync{Status: "synced", ObservedAt: observedAt}
	for _, resource := range resources {
		for _, field := range resource.Configuration {
			switch field.State {
			case "synced":
				summary.SyncedFields++
			case "pending":
				summary.PendingFields++
			case "drifted":
				summary.DriftedFields++
			case "conflict":
				summary.ConflictFields++
			}
		}
	}
	summary.BlockingChanges = summary.DriftedFields > 0 || summary.ConflictFields > 0
	switch {
	case summary.ConflictFields > 0:
		summary.Status = "conflict"
	case summary.DriftedFields > 0:
		summary.Status = "drifted"
	case summary.PendingFields > 0:
		summary.Status = "pending"
	}
	return summary
}

func (snapshot Snapshot) CloudDeploymentPreflightError() error {
	if snapshot.CloudSync.Unavailable > 0 {
		return fmt.Errorf("有 %d 个 AWS 资源无法读取实际参数；为避免用过期配置覆盖云上资源，平台已停止部署", snapshot.CloudSync.Unavailable)
	}
	if snapshot.CloudSync.BlockingChanges {
		return fmt.Errorf("检测到 AWS 控制台变更：%d 个漂移字段、%d 个冲突字段；平台已停止部署，不会用旧值覆盖 AWS；请先查看差异并选择“采用 AWS 实际配置”", snapshot.CloudSync.DriftedFields, snapshot.CloudSync.ConflictFields)
	}
	unstable := make([]string, 0)
	for _, resource := range snapshot.Resources {
		if len(resource.Configuration) == 0 {
			continue
		}
		status := stringFrom(resource.Metadata["aws_status"])
		if status != "" && !cloudResourceStable(status) {
			unstable = append(unstable, resource.DisplayName+"（"+status+"）")
		}
	}
	if len(unstable) > 0 {
		sort.Strings(unstable)
		return fmt.Errorf("AWS 资源仍在变更中：%s；等待状态稳定后再部署，避免连续修改造成容量叠加或中间态误判", strings.Join(unstable, "、"))
	}
	if err := cloudCapacityTransitionError(snapshot.Resources); err != nil {
		return err
	}
	return nil
}

func cloudCapacityTransitionError(resources []Resource) error {
	for _, resource := range resources {
		fields := make(map[string]ConfigurationField, len(resource.Configuration))
		for _, field := range resource.Configuration {
			fields[field.Path] = field
		}
		switch resource.Key {
		case "rds", "postgres":
			base := "data_services." + resource.Key + "."
			if field, found := fields[base+"allocated_storage"]; found && field.State == "pending" && intFrom(field.Desired) < intFrom(field.Actual) {
				return fmt.Errorf("%s 已分配存储不支持缩容：AWS 当前为 %d GiB，平台目标为 %d GiB；请恢复为当前值或更大值", resource.DisplayName, intFrom(field.Actual), intFrom(field.Desired))
			}
			allocated := fields[base+"allocated_storage"]
			if maximum, found := fields[base+"max_allocated_storage"]; found && intFrom(maximum.Desired) > 0 && intFrom(maximum.Desired) < intFrom(allocated.Actual) {
				return fmt.Errorf("%s 最大自动扩容容量不能低于 AWS 当前已分配容量：当前 %d GiB，平台上限 %d GiB", resource.DisplayName, intFrom(allocated.Actual), intFrom(maximum.Desired))
			}
		case "msk":
			for path, label := range map[string]string{
				"data_services.msk.broker_count": "Broker 数量",
				"data_services.msk.volume_size":  "单 Broker 磁盘",
			} {
				field, found := fields[path]
				if found && field.State == "pending" && intFrom(field.Desired) < intFrom(field.Actual) {
					return fmt.Errorf("Amazon MSK %s只支持扩容：AWS 当前为 %d，平台目标为 %d；如需缩容请新建集群并迁移数据", label, intFrom(field.Actual), intFrom(field.Desired))
				}
			}
		case "aurora":
			backtrackEnabled, found := fields["data_services.aurora.backtrack_enabled"]
			if found && backtrackEnabled.State == "pending" && boolFrom(backtrackEnabled.Desired) && !boolFrom(backtrackEnabled.Actual) {
				return errors.New("Aurora 回溯无法对创建时未启用回溯的已有集群直接补开；请在新建集群或从快照恢复时启用，平台已在 Terraform 执行前停止")
			}
		}
		for path, label := range map[string]string{
			"data_services.elasticache.mode":          "ElastiCache 运行模式",
			"data_services.msk.mode":                  "MSK 运行模式",
			"data_services.amazon_mq.deployment_mode": "Amazon MQ 部署模式",
		} {
			field, found := fields[path]
			if found && field.State == "pending" && !cloudValuesEqual(field.Desired, field.Actual) {
				return fmt.Errorf("%s 不支持对现有资源原地切换（AWS 当前 %v，平台目标 %v）；请新建目标模式资源并完成数据迁移后再下线旧资源", label, field.Actual, field.Desired)
			}
		}
	}
	return nil
}

func cloudFieldSpecs(key string, doc environment.Document) []cloudFieldSpec {
	specs := map[string][]cloudFieldSpec{
		"eks": {
			{"eks.kubernetes_version", "Kubernetes 版本", true},
			{"eks.endpoint_private_access", "私网 API", true},
			{"eks.endpoint_public_access", "公网 API", true},
			{"eks.public_access_cidrs", "API 公网白名单（仅追加）", false},
		},
		"rds": rdsFieldSpecs("rds"), "postgres": rdsFieldSpecs("postgres"),
		"aurora": {
			{"data_services.aurora.enabled", "启用状态", true},
			{"data_services.aurora.engine", "引擎", false}, {"data_services.aurora.engine_version", "引擎版本", true},
			{"data_services.aurora.database_name", "数据库", false}, {"data_services.aurora.master_username", "管理员", false},
			{"data_services.aurora.port", "端口", false}, {"data_services.aurora.instance_count", "实例数", true},
			{"data_services.aurora.min_acu", "最小 ACU", true}, {"data_services.aurora.max_acu", "最大 ACU", true},
			{"data_services.aurora.backup_retention_days", "备份保留天数", true}, {"data_services.aurora.backup_window", "备份窗口", true},
			{"data_services.aurora.maintenance_window", "维护窗口", true}, {"data_services.aurora.deletion_protection", "删除保护", true},
			{"data_services.aurora.backtrack_enabled", "启用回溯", true}, {"data_services.aurora.backtrack_window_hours", "回溯窗口（小时）", true},
			{"data_services.aurora.auto_minor_version_upgrade", "自动次版本升级", true},
			{"data_services.aurora.performance_insights_enabled", "Performance Insights", true},
		},
		"documentdb": {
			{"data_services.documentdb.enabled", "启用状态", true},
			{"data_services.documentdb.engine", "引擎", false}, {"data_services.documentdb.engine_version", "引擎版本", true},
			{"data_services.documentdb.instance_class", "实例规格", true}, {"data_services.documentdb.instance_count", "实例数", true},
			{"data_services.documentdb.master_username", "管理员", false}, {"data_services.documentdb.port", "端口", false},
			{"data_services.documentdb.storage_type", "存储类型", true},
			{"data_services.documentdb.backup_retention_days", "备份保留天数", true}, {"data_services.documentdb.auto_minor_version_upgrade", "自动次版本升级", true},
			{"data_services.documentdb.deletion_protection", "删除保护", true},
		},
		"elasticache": {
			{"data_services.elasticache.enabled", "启用状态", true},
			{"data_services.elasticache.mode", "运行模式", false}, {"data_services.elasticache.engine", "引擎", false},
			{"data_services.elasticache.engine_version", "引擎版本", true}, {"data_services.elasticache.node_type", "节点规格", true},
			{"data_services.elasticache.num_node_groups", "分片数量", true}, {"data_services.elasticache.nodes_per_shard", "每分片总节点数", true},
			{"data_services.elasticache.parameter_group_name", "参数组", true}, {"data_services.elasticache.snapshot_retention_days", "快照保留天数", true},
			{"data_services.elasticache.snapshot_window", "快照窗口", true}, {"data_services.elasticache.maintenance_window", "维护窗口", true},
			{"data_services.elasticache.tls_enabled", "传输加密", true}, {"data_services.elasticache.auto_minor_version_upgrade", "自动次版本升级", true},
			{"data_services.elasticache.max_storage_gb", "最大存储 GB", true}, {"data_services.elasticache.max_ecpu", "最大 ECPU", true},
		},
		"msk": {
			{"data_services.msk.enabled", "启用状态", true},
			{"data_services.msk.mode", "运行模式", false}, {"data_services.msk.kafka_version", "Kafka 版本", true},
			{"data_services.msk.instance_type", "Broker 规格", true}, {"data_services.msk.broker_count", "Broker 数量", true},
			{"data_services.msk.volume_size", "单 Broker 磁盘", true}, {"data_services.msk.enhanced_monitoring", "监控级别", true},
		},
		"amazon_mq": {
			{"data_services.amazon_mq.enabled", "启用状态", true},
			{"data_services.amazon_mq.engine", "引擎", false}, {"data_services.amazon_mq.engine_version", "引擎版本", true},
			{"data_services.amazon_mq.deployment_mode", "部署模式", true}, {"data_services.amazon_mq.host_instance_type", "实例规格", true},
			{"data_services.amazon_mq.auto_minor_version_upgrade", "自动次版本升级", true},
			{"data_services.amazon_mq.general_logs_enabled", "通用日志", true},
		},
		"ecr": {
			{"ecr.enabled", "启用状态", true},
			{"ecr.repositories", "仓库列表（仅追加）", false}, {"ecr.image_tag_mutability", "Tag 可变性", true}, {"ecr.scan_on_push", "推送扫描", true},
		},
	}
	result := append([]cloudFieldSpec(nil), specs[key]...)
	if key == "eks" && environment.IsExistingEKS(doc) {
		for index := range result {
			result[index].Syncable = false
		}
		return result
	}
	if key == "eks" && !environment.IsExistingEKS(doc) {
		groupNames := sortedMapKeys(valuePath(doc, "eks.node_groups"))
		for _, group := range groupNames {
			base := "eks.node_groups." + group + "."
			result = append(result,
				cloudFieldSpec{base + "instance_types", group + " · 实例类型", true},
				cloudFieldSpec{base + "capacity_type", group + " · 容量类型", true},
				cloudFieldSpec{base + "ami_type", group + " · AMI 类型", true},
				cloudFieldSpec{base + "min_size", group + " · 最小节点", true},
				cloudFieldSpec{base + "desired_size", group + " · 当前期望节点", false},
				cloudFieldSpec{base + "max_size", group + " · 最大节点", true},
				cloudFieldSpec{base + "disk_size", group + " · 系统盘 GiB", true},
			)
		}
	}
	return result
}

func rdsFieldSpecs(key string) []cloudFieldSpec {
	base := "data_services." + key + "."
	return []cloudFieldSpec{
		{base + "enabled", "启用状态", true},
		{base + "engine", "引擎", false}, {base + "engine_version", "引擎版本", true},
		{base + "instance_class", "实例规格", true}, {base + "database_name", "数据库", false},
		{base + "master_username", "管理员", false}, {base + "port", "端口", false},
		{base + "allocated_storage", "已分配存储 GB", true}, {base + "max_allocated_storage", "最大自动扩容 GB", true},
		{base + "multi_az", "Multi-AZ", true}, {base + "backup_retention_days", "备份保留天数", true},
		{base + "backup_window", "备份窗口", true}, {base + "maintenance_window", "维护窗口", true},
		{base + "auto_minor_version_upgrade", "自动次版本升级", true}, {base + "performance_insights_enabled", "Performance Insights", true},
		{base + "deletion_protection", "删除保护", true},
	}
}

func snapshotHasCloudResource(snapshot Snapshot, key string) bool {
	for _, resource := range snapshot.Resources {
		if resource.Key == key && (len(resource.Configuration) > 0 || len(resource.Baseline) > 0 || len(resource.AccessPoints) > 0) {
			return true
		}
	}
	return false
}

func cloudResourceDesiredEnabled(key string, doc environment.Document) bool {
	if key == "eks" {
		return true
	}
	if key == "ecr" {
		return boolFrom(mapPath(doc, "ecr")["enabled"])
	}
	return boolFrom(mapFrom(mapPath(doc, "data_services")[key])["enabled"])
}

func cloudConfigurationResourceKey(key string) bool {
	switch key {
	case "eks", "rds", "postgres", "aurora", "documentdb", "elasticache", "msk", "amazon_mq", "ecr":
		return true
	default:
		return false
	}
}

func cloudFieldState(desired, actual, baseline any) string {
	if cloudValuesEqual(desired, actual) {
		return "synced"
	}
	actualChanged := !cloudValuesEqual(actual, baseline)
	desiredChanged := !cloudValuesEqual(desired, baseline)
	switch {
	case actualChanged && desiredChanged:
		return "conflict"
	case actualChanged:
		return "drifted"
	default:
		return "pending"
	}
}

func additiveStringSetState(desired, actual any) string {
	desiredValues := canonicalStringSet(desired)
	actualValues := canonicalStringSet(actual)
	for value := range desiredValues {
		if _, exists := actualValues[value]; !exists {
			return "pending"
		}
	}
	return "synced"
}

func additiveCloudFieldPath(path string) bool {
	switch path {
	case "eks.public_access_cidrs", "ecr.repositories":
		return true
	default:
		return false
	}
}

func canonicalStringSet(value any) map[string]struct{} {
	result := make(map[string]struct{})
	switch items := canonicalCloudValue(value).(type) {
	case []string:
		for _, item := range items {
			if item = strings.TrimSpace(item); item != "" {
				result[item] = struct{}{}
			}
		}
	case string:
		if items != "" {
			result[items] = struct{}{}
		}
	}
	return result
}

func cloudValuesEqual(left, right any) bool {
	return reflect.DeepEqual(canonicalCloudValue(left), canonicalCloudValue(right))
}

func canonicalCloudValue(value any) any {
	switch typed := value.(type) {
	case json.Number:
		if integer, err := typed.Int64(); err == nil {
			return integer
		}
		if decimal, err := typed.Float64(); err == nil {
			return decimal
		}
	case float64:
		if typed == float64(int64(typed)) {
			return int64(typed)
		}
	case int:
		return int64(typed)
	case []string:
		items := append([]string(nil), typed...)
		sort.Strings(items)
		return items
	case []any:
		items := make([]string, 0, len(typed))
		for _, item := range typed {
			items = append(items, stringFrom(item))
		}
		sort.Strings(items)
		return items
	case string:
		return strings.TrimSpace(typed)
	}
	return value
}

func (s *Service) awsObject(ctx context.Context, project, region string, args ...string) (map[string]any, error) {
	commandEnvironment, err := s.projectCommandEnvironment(ctx, project)
	if err != nil {
		return nil, err
	}
	args = append(args, "--region", region, "--output", "json", "--no-cli-pager")
	cmd := exec.CommandContext(ctx, s.config.Tools.AWS, args...) // #nosec G204 -- no shell is used; identifiers come from validated environment configuration.
	cmd.Env = commandEnvironment
	payload, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(payload))
		if len(message) > 1200 {
			message = message[len(message)-1200:]
		}
		return nil, fmt.Errorf("AWS %s: %w: %s", strings.Join(args[:minInt(len(args), 2)], " "), err, message)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var result map[string]any
	if err := decoder.Decode(&result); err != nil {
		return nil, fmt.Errorf("decode AWS response: %w", err)
	}
	return result, nil
}

func copyFields(target map[string]any, base string, source map[string]any, fields map[string]string) {
	for targetName, sourcePath := range fields {
		if value := valueAt(source, sourcePath); value != nil {
			target[base+targetName] = value
		}
	}
}

func setUniformCloudField(target map[string]any, path string, value any) error {
	if value == nil {
		return nil
	}
	if existing, found := target[path]; found && !cloudValuesEqual(existing, value) {
		return fmt.Errorf("检测到 %v 与 %v", existing, value)
	}
	target[path] = value
	return nil
}

func valueAt(source map[string]any, path string) any {
	var current any = source
	for _, part := range strings.Split(path, ".") {
		object := objectMap(current)
		if len(object) == 0 {
			return nil
		}
		current = object[part]
	}
	return current
}

func objectMap(value any) map[string]any {
	result, _ := value.(map[string]any)
	return result
}

func objectSlice(value any) []map[string]any {
	raw, _ := value.([]any)
	result := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if object := objectMap(item); object != nil {
			result = append(result, object)
		}
	}
	return result
}

func stringSlice(value any) []string {
	raw, _ := value.([]any)
	result := make([]string, 0, len(raw))
	for _, item := range raw {
		if text := stringFrom(item); text != "" {
			result = append(result, text)
		}
	}
	return result
}

func sortedStrings(value any) []string {
	items := stringSlice(value)
	sort.Strings(items)
	return items
}

func cloneAnyMap(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func firstNonEmpty(values ...any) any {
	for _, value := range values {
		if stringFrom(value) != "" {
			return value
		}
	}
	return nil
}

func isAWSNotFound(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "notfound") || strings.Contains(message, "not found") ||
		strings.Contains(message, "resourcenotfound") || strings.Contains(message, "dbinstancenotfound") ||
		strings.Contains(message, "dbclusternotfound") || strings.Contains(message, "replicationgroupnotfound")
}

func normalizeAWSReadError(err error) error {
	if isAWSNotFound(err) {
		return nil
	}
	return err
}

func cloudRuntimeStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "active", "available", "running":
		return "healthy"
	case "creating", "modifying", "updating", "rebooting", "maintenance", "pending":
		return "pending"
	case "", "deleting", "failed", "inaccessible-encryption-credentials", "restore-error":
		return "missing"
	default:
		return "drift"
	}
}

func cloudResourceStable(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "active", "available", "running":
		return true
	default:
		return false
	}
}

func cloudResourceDisplayName(key string) string {
	return map[string]string{
		"eks": "Amazon EKS", "rds": "RDS 管理数据库", "postgres": "RDS PostgreSQL",
		"aurora": "Aurora 游戏数据库", "documentdb": "Amazon DocumentDB（MongoDB 兼容）",
		"elasticache": "ElastiCache Redis/Valkey", "msk": "Amazon MSK Kafka",
		"amazon_mq": "Amazon MQ RabbitMQ", "ecr": "Amazon ECR",
	}[key]
}

func cloudResourceCategory(key string) string {
	if key == "eks" {
		return "容器平台"
	}
	if key == "ecr" {
		return "镜像仓库"
	}
	return "中间件与数据库"
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
