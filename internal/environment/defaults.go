package environment

import (
	"strconv"
	"strings"
)

const dataServiceChartVersion = "0.1.1"
const rabbitMQChartVersion = "0.2.0"
const etcdWorkbenchChartVersion = "0.1.0"
const bytebaseChartVersion = "0.1.2"
const etcdWorkbenchAppVersion = "1.1.4"
const etcdWorkbenchImage = "tzfun/etcd-workbench:1.1.4@sha256:c58de0e1b96ebdc01856c8ef87d9cd6f2113e4d8acdd32965e5d3c6cdc949b71"
const lokiChartVersion = "6.39.0"
const openTelemetryCollectorChartVersion = "0.169.0"
const observabilityOTelChartVersion = "0.2.0"
const jaegerStackChartVersion = "0.1.1"
const tempoChartVersion = "1.24.4"
const clickVisualStackChartVersion = "0.3.5"
const efkStackChartVersion = "0.1.2"
const clickVisualImage = "clickvisual/clickvisual:1.0.4@sha256:e11810796f15e8e7c47c9003c058356485b9d5fc71930313145a373c3658675e"
const alertTemplatePresetVersion = 6

// DefaultDocument is the safe, editable starting point for a newly created
// project environment. It intentionally enables only the EKS platform
// prerequisites; paid data services and self-hosted components remain opt-in.
func DefaultDocument(project, environmentName string) Document {
	region := "ap-south-1"
	publicEndpoint := environmentName != "prod"
	publicAccessCIDRs := []any{"0.0.0.0/0"}
	if !publicEndpoint {
		publicAccessCIDRs = []any{}
	}
	zones := []any{"ap-south-1a", "ap-south-1b", "ap-south-1c"}
	catalog := map[string]any{
		"jenkins":                 catalogComponent("Jenkins", "CICD", "https://charts.jenkins.io", "jenkins", "5.9.34", "jenkins", 8080, "http", "admin", "jenkins", "jenkins-admin-password"),
		"argocd":                  catalogComponent("Argo CD", "CICD", "https://argoproj.github.io/argo-helm", "argo-cd", "9.4.15", "argocd-server", 80, "http", "admin", "argocd-initial-admin-secret", "password"),
		"tekton":                  catalogComponent("Tekton Pipelines", "CICD", "https://cdfoundation.github.io/tekton-helm-chart", "tekton-pipeline", "1.12.2", "tekton-dashboard", 9097, "http", "", "", ""),
		"gitlab":                  catalogComponent("GitLab", "CICD", "https://charts.gitlab.io", "gitlab", "9.2.1", "gitlab-webservice-default", 8181, "http", "root", "gitlab-gitlab-initial-root-password", "password"),
		"nacos":                   catalogComponent("Nacos", "配置与注册中心", "https://nacos-group.github.io/nacos-k8s/helm", "nacos", "1.0.0", "nacos-cs", 8080, "http", "", "", ""),
		"xxl_job":                 catalogComponent("XXL-JOB Admin", "调度", "", "xxl-job", "0.1.0", "xxl-job", 8080, "http", "admin", "xxl-job-admin", "password"),
		"kafka":                   catalogComponent("Kafka（自建）", "消息队列", "oci://registry-1.docker.io/bitnamicharts", "kafka", "32.3.14", "kafka", 9092, "kafka", "user1", "kafka-user-passwords", "client-passwords"),
		"rabbitmq":                catalogComponent("RabbitMQ（自建）", "消息队列", "", "rabbitmq", rabbitMQChartVersion, "rabbitmq", 5672, "amqp", "user", "rabbitmq", "rabbitmq-password"),
		"bytebase":                catalogComponent("Bytebase 数据库管理", "数据库管理", "", "bytebase", bytebaseChartVersion, "bytebase", 8080, "http", "admin@ops-deploy.local", "bytebase", "admin-password"),
		"redisinsight":            catalogComponent("RedisInsight", "数据库管理", "", "redisinsight", "0.1.0", "redisinsight", 80, "http", "admin", "redisinsight", "admin-password"),
		"etcd_workbench":          catalogComponent("Etcd Workbench", "配置与注册中心", "", "etcd-workbench", etcdWorkbenchChartVersion, "etcd-workbench", 8002, "http", "admin", "etcd-workbench", "password"),
		"prometheus":              catalogComponent("Prometheus + Grafana", "监控", "https://prometheus-community.github.io/helm-charts", "kube-prometheus-stack", "75.15.1", "prometheus-grafana", 80, "http", "admin", "prometheus-grafana", "admin-password"),
		"opentelemetry_collector": catalogComponent("OpenTelemetry Collector", "监控", "https://open-telemetry.github.io/opentelemetry-helm-charts", "opentelemetry-collector", openTelemetryCollectorChartVersion, "opentelemetry-collector", 4317, "grpc", "", "", ""),
		"jaeger":                  catalogComponent("Jaeger 链路追踪", "监控", "", "jaeger-stack", jaegerStackChartVersion, "jaeger", 80, "http", "admin", "jaeger-access", "password"),
		"tempo":                   catalogComponent("Tempo 链路追踪", "监控", "https://grafana.github.io/helm-charts", "tempo", tempoChartVersion, "tempo", 3200, "http", "", "", ""),
		"loki":                    catalogComponent("Loki", "日志", "https://grafana.github.io/helm-charts", "loki", lokiChartVersion, "loki-gateway", 80, "http", "", "", ""),
		"clickvisual_stack":       catalogComponent("ClickVisual 日志平台", "日志", "", "clickvisual-stack", clickVisualStackChartVersion, "clickvisual", 80, "http", "admin", "clickvisual-stack-access", "admin-password"),
		"efk_stack":               catalogComponent("EFK 日志系统", "日志", "", "efk-stack", efkStackChartVersion, "efk-kibana", 5601, "http", "elastic", "efk-stack-access", "elastic-password"),
		"higress":                 catalogComponent("Higress 网关", "网关", "https://higress.cn/helm-charts", "higress", "2.1.7", "higress-console", 8080, "http", "admin", "higress-console", "adminPassword"),
		"nginx_ingress":           catalogComponent("F5 NGINX Ingress Controller", "网关", "https://helm.nginx.com/stable", "nginx-ingress", "2.3.1", "nginx-ingress-controller", 80, "http", "", "", ""),
		"mysql":                   catalogComponent("MySQL（自建）", "中间件与数据库", "", "data-service", dataServiceChartVersion, "mysql", 3306, "mysql", "root", "mysql-auth", "password"),
		"redis":                   catalogComponent("Redis（自建）", "中间件与数据库", "", "data-service", dataServiceChartVersion, "redis", 6379, "redis", "default", "redis-auth", "password"),
		"activemq":                catalogComponent("ActiveMQ（自建）", "中间件与数据库", "", "data-service", dataServiceChartVersion, "activemq", 61616, "tcp", "admin", "activemq-auth", "password"),
		"mongodb":                 catalogComponent("MongoDB（自建）", "中间件与数据库", "", "data-service", dataServiceChartVersion, "mongodb", 27017, "mongodb", "root", "mongodb-auth", "password"),
	}
	catalog["xxl_job"].(map[string]any)["builtin_chart"] = "xxl-job"
	catalog["nacos"].(map[string]any)["builtin_chart"] = "nacos"
	catalog["rabbitmq"].(map[string]any)["builtin_chart"] = "rabbitmq"
	catalog["rabbitmq"].(map[string]any)["console_service_name"] = "rabbitmq"
	catalog["rabbitmq"].(map[string]any)["console_service_port"] = 15672
	catalog["rabbitmq"].(map[string]any)["console_protocol"] = "http"
	catalog["rabbitmq"].(map[string]any)["values"] = map[string]any{
		"auth":        map[string]any{"username": "user"},
		"persistence": map[string]any{"enabled": true, "storageClass": "gp3", "size": "8Gi", "retainOnDelete": true},
		"resources": map[string]any{
			"requests": map[string]any{"cpu": "250m", "memory": "512Mi"},
			"limits":   map[string]any{"cpu": "1", "memory": "1Gi"},
		},
	}
	catalog["bytebase"].(map[string]any)["builtin_chart"] = "bytebase"
	catalog["bytebase"].(map[string]any)["standalone_only"] = true
	catalog["bytebase"].(map[string]any)["app_version"] = "3.20.1"
	catalog["bytebase"].(map[string]any)["app_versions"] = []any{"3.20.1", "3.20.0", "3.19.1"}
	catalog["bytebase"].(map[string]any)["values"] = map[string]any{
		"image":       map[string]any{"repository": "bytebase/bytebase", "tag": "3.20.1", "pullPolicy": "IfNotPresent"},
		"persistence": map[string]any{"enabled": true, "storageClass": "gp3", "size": "20Gi", "retainOnDelete": true},
		"admin":       map[string]any{"email": "admin@ops-deploy.local", "title": "Platform Admin"},
		"resources": map[string]any{
			"requests": map[string]any{"cpu": "250m", "memory": "512Mi"},
			"limits":   map[string]any{"cpu": "2", "memory": "2Gi"},
		},
	}
	catalog["redisinsight"].(map[string]any)["builtin_chart"] = "redisinsight"
	catalog["redisinsight"].(map[string]any)["standalone_only"] = true
	catalog["redisinsight"].(map[string]any)["app_version"] = "3.8.0"
	catalog["redisinsight"].(map[string]any)["app_versions"] = []any{"3.8.0", "3.6.0", "3.4.2"}
	catalog["redisinsight"].(map[string]any)["values"] = map[string]any{
		"image":       map[string]any{"repository": "redis/redisinsight", "tag": "3.8.0", "pullPolicy": "IfNotPresent"},
		"basicAuth":   map[string]any{"username": "admin"},
		"persistence": map[string]any{"enabled": true, "storageClass": "gp3", "size": "5Gi", "retainOnDelete": true},
		"resources": map[string]any{
			"requests": map[string]any{"cpu": "100m", "memory": "256Mi"},
			"limits":   map[string]any{"cpu": "1", "memory": "1Gi"},
		},
	}
	configureEtcdWorkbench(catalog)
	for _, key := range []string{"mysql", "redis", "activemq", "mongodb"} {
		catalog[key].(map[string]any)["builtin_chart"] = "data-service"
	}
	configureDataServiceCatalog(catalog)
	// The management console is a ClusterIP Service while traffic enters
	// through a separate public LoadBalancer Service.
	catalog["higress"].(map[string]any)["public_service_name"] = "higress-gateway"
	// Always create managed EKS Higress NLBs with a platform-owned frontend
	// security group. AWS cannot add security-group support to an NLB that was
	// created without one, so this must be part of the first Service reconcile.
	// The initial public CIDR preserves the behavior of a normal internet-facing
	// gateway; operators can narrow it to Cloudflare or office CIDRs in the UI.
	catalog["higress"].(map[string]any)["nlb"] = map[string]any{
		"security_group_mode":                 "managed",
		"security_group_ids":                  []any{},
		"manage_backend_security_group_rules": true,
		"scheme":                              "internet-facing",
		"allowed_ports":                       []any{80, 443},
		"allowed_cidrs":                       []any{"0.0.0.0/0"},
		"external_traffic_policy":             "Local",
		"idle_timeout_seconds":                600,
	}
	// The upstream Higress chart requests two full CPU cores for the gateway.
	// A two-vCPU EKS node exposes less than 2000m after kubelet reservation, so
	// that default can never schedule on the platform's m7i.large test/UAT
	// gateway pool. Keep the limit at two cores while using a schedulable
	// request. Explicit user values are preserved by ApplyDefaults.
	catalog["higress"].(map[string]any)["values"] = map[string]any{
		"higress-core": map[string]any{
			"gateway": map[string]any{
				"resources": map[string]any{
					"requests": map[string]any{"cpu": "1", "memory": "1Gi"},
					"limits":   map[string]any{"cpu": "2", "memory": "2Gi"},
				},
			},
		},
	}
	// Keep gateway control-plane workloads isolated from business and shared
	// platform services. Existing environments retain their explicitly saved
	// namespace until they are migrated with the blue/green gateway procedure.
	catalog["higress"].(map[string]any)["namespace"] = "higress-system"
	catalog["nginx_ingress"].(map[string]any)["public_service_name"] = "nginx-ingress-controller"
	catalog["prometheus"].(map[string]any)["values"] = defaultPrometheusValues()
	catalog["opentelemetry_collector"].(map[string]any)["builtin_chart"] = "observability-otel"
	catalog["opentelemetry_collector"].(map[string]any)["chart_version"] = observabilityOTelChartVersion
	catalog["opentelemetry_collector"].(map[string]any)["values"] = defaultOpenTelemetryCollectorValues(project, environmentName)
	catalog["jaeger"].(map[string]any)["builtin_chart"] = "jaeger-stack"
	catalog["jaeger"].(map[string]any)["values"] = defaultJaegerValues(environmentName)
	catalog["jaeger"].(map[string]any)["console_service_name"] = "jaeger"
	catalog["jaeger"].(map[string]any)["console_service_port"] = 80
	catalog["jaeger"].(map[string]any)["console_protocol"] = "http"
	catalog["tempo"].(map[string]any)["values"] = defaultTempoValues(environmentName)
	catalog["tempo"].(map[string]any)["standalone_only"] = true
	catalog["loki"].(map[string]any)["values"] = defaultLokiValues()
	configureClickVisualStack(catalog, project, environmentName)
	configureEFKStack(catalog, project, environmentName)
	replicaPaths := map[string][]any{
		"argocd":                  {"controller.replicas", "server.replicas", "repoServer.replicas", "applicationSet.replicas"},
		"gitlab":                  {"gitlab.webservice.minReplicas", "gitlab.sidekiq.minReplicas", "gitlab.gitlab-shell.minReplicas", "registry.hpa.minReplicas"},
		"nacos":                   {"replicaCount"},
		"xxl_job":                 {"replicaCount"},
		"kafka":                   {"controller.replicaCount"},
		"rabbitmq":                {"replicaCount"},
		"prometheus":              {"prometheus.prometheusSpec.replicas", "alertmanager.alertmanagerSpec.replicas"},
		"opentelemetry_collector": {"replicaCount"},
		"jaeger":                  {"replicaCount"},
		"loki":                    {"singleBinary.replicas"},
		// Gateway capacity and console capacity are separate concerns. Scaling a
		// public gateway must never multiply the administration console replicas.
		"higress":       {"higress-core.gateway.replicas"},
		"nginx_ingress": {"controller.replicaCount"},
		"mysql":         {"replicaCount"},
		"redis":         {"replicaCount"},
		"activemq":      {"replicaCount"},
		"mongodb":       {"replicaCount"},
	}
	for key, paths := range replicaPaths {
		catalog[key].(map[string]any)["replica_paths"] = paths
	}
	// The official Tekton pipeline chart has no dashboard Service. Keep the
	// release visible in status without publishing a non-existent endpoint.
	catalog["tekton"].(map[string]any)["service_name"] = ""
	catalog["tekton"].(map[string]any)["service_port"] = 0
	return Document{
		"project": project, "environment": environmentName, "region": region,
		"data_service_defaults_version": 3,
		"efk_stack_defaults_version":    2,
		"deployment_target": map[string]any{
			"type": TargetManaged, "cluster_name": "", "manage_addons": true,
			"namespace_prefix": namespacePrefix(project, environmentName),
		},
		"tags": map[string]any{"Owner": "ops-team", "CostCenter": project + "-" + environmentName},
		"network": map[string]any{
			"mode": "create", "existing_vpc_id": "", "existing_vpc_cidr": "",
			"existing_workload_subnet_ids": []any{}, "existing_data_subnet_ids": []any{},
			"vpc_cidr": "10.40.0.0/16", "service_ipv4_cidr": "172.20.0.0/16",
			"availability_zones": zones, "workload_subnet_type": "private", "data_subnet_type": "public",
			"workload_subnet_zones": zones, "data_subnet_zones": zones,
			"nat_gateway_mode":   "always",
			"single_nat_gateway": true,
			"public_subnets":     map[string]any{"ap-south-1a": "10.40.0.0/20", "ap-south-1b": "10.40.16.0/20", "ap-south-1c": "10.40.32.0/20"},
			"private_subnets":    map[string]any{"ap-south-1a": "10.40.64.0/20", "ap-south-1b": "10.40.80.0/20", "ap-south-1c": "10.40.96.0/20"},
		},
		"eks": map[string]any{
			"kubernetes_version": "1.34", "endpoint_private_access": true, "endpoint_public_access": publicEndpoint,
			"public_access_cidrs": publicAccessCIDRs, "enabled_control_plane_logs": []any{"api", "audit", "authenticator", "controllerManager", "scheduler"},
			"log_retention_days": 30, "admin_principal_arns": []any{},
			"workload_scheduling": map[string]any{"enabled": true},
			"node_groups": map[string]any{
				"ingress-gateway": map[string]any{
					"availability_zones": zones, "instance_types": []any{"m7i.large", "m6i.large"}, "capacity_type": "ON_DEMAND",
					"min_size": 2, "desired_size": 2, "max_size": 6, "disk_size": 80, "capacity_deferred": false,
					"subnet_type": "private",
					"labels": map[string]any{
						"workload-class":     "gateway",
						"ops-deploy.io/pool": "ingress-gateway",
					},
					"taints": []any{},
				},
				"business-workload": map[string]any{
					"availability_zones": zones, "instance_types": []any{"m7i.large", "m6i.large"}, "capacity_type": "ON_DEMAND",
					"min_size": 1, "desired_size": 2, "max_size": 6, "disk_size": 80, "capacity_deferred": false,
					"subnet_type": "private",
					"labels": map[string]any{
						"workload-class":     "application",
						"ops-deploy.io/pool": "business-workload",
					},
					"taints": []any{map[string]any{"key": "workload-class", "value": "application", "effect": "NO_SCHEDULE"}},
				},
				"platform-ops": map[string]any{
					"availability_zones": zones, "instance_types": []any{"m7i.large", "m6i.large"}, "capacity_type": "ON_DEMAND",
					"min_size": 1, "desired_size": 1, "max_size": 3, "disk_size": 80, "capacity_deferred": false,
					"subnet_type": "private",
					"labels": map[string]any{
						"workload-class":     "platform",
						"ops-deploy.io/pool": "platform-ops",
					},
					"taints": []any{},
				},
			},
		},
		"namespaces": map[string]any{},
		"data_services": map[string]any{
			"rds":         map[string]any{"enabled": false, "engine": "mysql", "engine_version": "8.0", "mode": "instance", "instance_class": "db.t4g.medium", "database_name": "ops_admin", "master_username": "ops_admin", "credential_management": "self-managed", "port": 3306, "allocated_storage": 100, "max_allocated_storage": 500, "multi_az": false, "backup_retention_days": 7, "backup_window": "18:00-19:00", "maintenance_window": "sun:19:00-sun:20:00", "auto_minor_version_upgrade": false, "performance_insights_enabled": true, "apply_immediately": true, "deletion_protection": false, "skip_final_snapshot": true, "network_type": "public"},
			"aurora":      map[string]any{"enabled": false, "engine": "aurora-mysql", "engine_version": "8.0.mysql_aurora.3.10.3", "mode": "serverless-v2", "database_name": "ops_game", "master_username": "ops_game", "credential_management": "self-managed", "port": 3306, "instance_count": 2, "min_acu": 1, "max_acu": 8, "backup_retention_days": 7, "backup_window": "17:00-18:00", "maintenance_window": "sun:18:00-sun:19:00", "backtrack_enabled": false, "backtrack_window_hours": 72, "tls_enabled": false, "auto_minor_version_upgrade": false, "performance_insights_enabled": true, "apply_immediately": true, "deletion_protection": false, "skip_final_snapshot": true, "network_type": "public"},
			"postgres":    map[string]any{"enabled": false, "engine": "postgres", "engine_version": "17.4", "mode": "instance", "instance_class": "db.t4g.medium", "database_name": "ops_app", "master_username": "ops_app", "port": 5432, "allocated_storage": 100, "max_allocated_storage": 500, "multi_az": false, "backup_retention_days": 7, "backup_window": "18:00-19:00", "maintenance_window": "sun:19:00-sun:20:00", "auto_minor_version_upgrade": false, "performance_insights_enabled": true, "apply_immediately": true, "deletion_protection": false, "skip_final_snapshot": true, "network_type": "public"},
			"documentdb":  map[string]any{"enabled": false, "engine": "docdb", "engine_version": "5.0.0", "mode": "provisioned-cluster", "instance_class": "db.t3.medium", "instance_count": 1, "master_username": "docdbadmin", "port": 27017, "storage_type": "standard", "backup_retention_days": 7, "auto_minor_version_upgrade": false, "apply_immediately": true, "deletion_protection": false, "skip_final_snapshot": true, "tls_enabled": true, "network_type": "public"},
			"elasticache": map[string]any{"enabled": false, "engine": "valkey", "engine_version": "8.2", "mode": "cluster", "node_type": "cache.t4g.small", "port": 6379, "tls_enabled": false, "num_node_groups": 2, "nodes_per_shard": 2, "replicas_per_node_group": 1, "parameter_group_name": "default.valkey8.cluster.on", "snapshot_retention_days": 3, "snapshot_window": "16:00-17:00", "maintenance_window": "sun:17:00-sun:18:00", "auto_minor_version_upgrade": false, "apply_immediately": true, "network_type": "public"},
			"msk":         map[string]any{"enabled": false, "mode": "serverless", "kafka_version": "3.9.x", "instance_type": "kafka.m7g.large", "broker_count": 3, "volume_size": 100, "enhanced_monitoring": "PER_BROKER", "port": 9098, "network_type": "public"},
			"amazon_mq":   map[string]any{"enabled": false, "engine": "RabbitMQ", "engine_version": "3.13", "deployment_mode": "SINGLE_INSTANCE", "host_instance_type": "mq.m7g.medium", "master_username": "opsadmin", "port": 5671, "tls_enabled": true, "auto_minor_version_upgrade": false, "general_logs_enabled": true, "apply_immediately": true, "network_type": "public"},
		},
		"ecr": map[string]any{"enabled": true, "force_delete": false, "scan_on_push": true, "image_tag_mutability": "IMMUTABLE", "untagged_expire_days": 7, "keep_image_count": 30, "repositories": []any{"platform-gateway", "platform-service", "game-service"}},
		"components": map[string]any{
			"eks_addons":                   map[string]any{"vpc_cni": true, "coredns": true, "kube_proxy": true, "pod_identity_agent": true, "ebs_csi_driver": true},
			"aws_load_balancer_controller": map[string]any{"enabled": true, "chart_version": "1.13.4"},
			"metrics_server":               map[string]any{"enabled": true, "chart_version": "3.13.1"},
			"cluster_autoscaler":           map[string]any{"enabled": true, "chart_version": "9.53.0"},
			"external_dns":                 map[string]any{"enabled": false, "chart_version": "1.19.0", "domain_filters": []any{}, "route53_zone_arns": []any{"*"}},
			"cert_manager":                 map[string]any{"enabled": false, "chart_version": "v1.18.2"},
			"consul":                       map[string]any{"enabled": false, "namespace": "platform-server", "deployment_mode": "cluster", "replicas": 3, "image": "hashicorp/consul:1.21.3", "chart_version": "1.8.0", "storage_class": "gp3", "storage_size": "20Gi", "retain_pvc_on_delete": true, "backup": map[string]any{"enabled": false, "schedule": "0 */6 * * *", "retention_days": 14}},
			"etcd":                         map[string]any{"enabled": false, "namespace": "platform-server", "deployment_mode": "cluster", "replicas": 3, "image": "quay.io/coreos/etcd:v3.6.4", "tls_enabled": false, "storage_class": "gp3", "storage_size": "20Gi", "retain_pvc_on_delete": true, "web_ui": map[string]any{"enabled": true, "username": "admin", "backend_image": "ghcr.io/etcdfinder/etcdfinder:latest@sha256:657d1bf0990c048a78905cceeea65e2d2ed95573dce9c9b91e5936d8be1a61b4", "frontend_image": "ghcr.io/etcdfinder/etcdfinder-ui:latest@sha256:479f1c63834000b824c1be500b7ebe0cf10065c5bdba19a3bfdc9dee1a2ab58a", "search_image": "getmeili/meilisearch:v1.15.2@sha256:fe500cf9cca05cb9f027981583f28eccf17d35d94499c1f8b7b844e7418152fc", "grpc_proxy_image": "envoyproxy/envoy:distroless-v1.38.0@sha256:a7a56545102f7a682e0cafea2c9b8448af1b09ebb710eab688dfb931e3ec7ff6", "auth_proxy_image": "nginx:1.28.0-alpine@sha256:30f1c0d78e0ad60901648be663a710bdadf19e4c10ac6782c235200619158284"}, "backup": map[string]any{"enabled": false, "schedule": "30 * * * *", "retention_days": 14}},
			"catalog":                      catalog,
		},
		"tls": map[string]any{
			"certificates": []any{},
		},
		"domains": []any{},
		"alerting": map[string]any{
			"enabled":                 false,
			"namespace":               "monitoring",
			"delivery_policy":         "core",
			"channels":                []any{},
			"templates":               defaultAlertTemplates(),
			"template_preset_version": alertTemplatePresetVersion,
		},
	}
}

func configureEtcdWorkbench(catalog map[string]any) {
	component := catalog["etcd_workbench"].(map[string]any)
	component["builtin_chart"] = "etcd-workbench"
	component["standalone_only"] = true
	component["app_version"] = etcdWorkbenchAppVersion
	component["app_versions"] = []any{etcdWorkbenchAppVersion}
	component["values"] = defaultEtcdWorkbenchValues()
}

func defaultEtcdWorkbenchValues() map[string]any {
	return map[string]any{
		"image": map[string]any{
			"repository": "tzfun/etcd-workbench",
			"tag":        strings.TrimPrefix(etcdWorkbenchImage, "tzfun/etcd-workbench:"),
			"pullPolicy": "IfNotPresent",
		},
		"basicAuth": map[string]any{"username": "admin"},
		"settings": map[string]any{
			"etcdExecuteTimeoutMillis": 5000,
			"enableHeartbeat":          true,
		},
		"persistence": map[string]any{
			"enabled": true, "storageClass": "gp3", "size": "5Gi", "retainOnDelete": true,
		},
		"resources": map[string]any{
			"requests": map[string]any{"cpu": "100m", "memory": "512Mi"},
			"limits":   map[string]any{"cpu": "1", "memory": "1Gi"},
		},
	}
}

func configureClickVisualStack(catalog map[string]any, project, environmentName string) {
	component := catalog["clickvisual_stack"].(map[string]any)
	prefix := namespacePrefix(project, environmentName)
	namespace := prefixedNamespace(prefix, "logs-system")
	component["builtin_chart"] = "clickvisual-stack"
	component["standalone_only"] = true
	component["namespace"] = namespace
	component["release_name"] = "clickvisual-stack"
	component["service_name"] = "clickvisual"
	component["service_port"] = 80
	component["console_service_name"] = "clickvisual"
	component["console_service_port"] = 80
	component["console_protocol"] = "http"
	component["timeout"] = 600
	component["values"] = map[string]any{
		"project":     project,
		"environment": environmentName,
		"namespace":   namespace,
		"images": map[string]any{
			"fluentBit":   "cr.fluentbit.io/fluent/fluent-bit:4.1.0",
			"kafka":       "apache/kafka:4.1.0",
			"clickhouse":  "clickhouse/clickhouse-server:26.3",
			"clickvisual": clickVisualImage,
			"mysql":       "mysql:8.4",
			"proxy":       "nginxinc/nginx-unprivileged:1.29.5-alpine",
			"curl":        "curlimages/curl:8.17.0",
		},
		"collection": map[string]any{
			"includeNamespaces": []any{},
			"excludeNamespaces": []any{},
			"includeServices":   []any{},
			"excludeServices":   []any{},
		},
		"kafka": map[string]any{
			"replicas": 1, "partitions": 6, "retentionHours": 24,
			"storage": map[string]any{"className": "gp3", "size": "50Gi", "activeClaims": []any{}},
			"resources": map[string]any{
				"requests": map[string]any{"cpu": "500m", "memory": "1Gi"},
				"limits":   map[string]any{"cpu": "2", "memory": "3Gi"},
			},
		},
		"clickhouse": map[string]any{
			"retentionDays": 7,
			"storage":       map[string]any{"className": "gp3", "size": "100Gi", "activeClaim": ""},
			"resources": map[string]any{
				"requests": map[string]any{"cpu": "1", "memory": "2Gi"},
				"limits":   map[string]any{"cpu": "4", "memory": "8Gi"},
			},
		},
		"mysql": map[string]any{
			"storage": map[string]any{"className": "gp3", "size": "20Gi", "activeClaim": ""},
			"resources": map[string]any{
				"requests": map[string]any{"cpu": "250m", "memory": "512Mi"},
				"limits":   map[string]any{"cpu": "2", "memory": "2Gi"},
			},
		},
		"clickvisual": map[string]any{
			"rootURL": "http://clickvisual", "username": "admin",
			"resources": map[string]any{
				"requests": map[string]any{"cpu": "250m", "memory": "512Mi"},
				"limits":   map[string]any{"cpu": "2", "memory": "2Gi"},
			},
		},
		"storage": map[string]any{
			"retainOnDelete":      true,
			"shrinkSafetyPercent": 30,
		},
	}
}

func configureEFKStack(catalog map[string]any, project, environmentName string) {
	component := catalog["efk_stack"].(map[string]any)
	prefix := namespacePrefix(project, environmentName)
	namespace := prefixedNamespace(prefix, "efk-system")
	component["builtin_chart"] = "efk-stack"
	component["standalone_only"] = true
	component["namespace"] = namespace
	component["release_name"] = "efk-stack"
	component["service_name"] = "efk-kibana"
	component["service_port"] = 5601
	component["console_service_name"] = "efk-kibana"
	component["console_service_port"] = 5601
	component["console_protocol"] = "http"
	component["timeout"] = 1800
	component["values"] = map[string]any{
		"project":     project,
		"environment": environmentName,
		"namespace":   namespace,
		"images": map[string]any{
			"elasticsearch": "docker.elastic.co/elasticsearch/elasticsearch:8.19.17",
			"fluentd":       "fluent/fluentd-kubernetes-daemonset:v1.19.3-debian-elasticsearch8-1.1@sha256:88da01d42636bb6f659f4116d66cafe44f7ee2436ddbd5c3b8bd595449f2a639",
			"kibana":        "docker.elastic.co/kibana/kibana:8.19.17",
			"curl":          "curlimages/curl:8.17.0",
		},
		"collection": map[string]any{
			"includeNamespaces": []any{},
			"excludeNamespaces": []any{},
			"includeServices":   []any{},
			"excludeServices":   []any{},
		},
		"elasticsearch": map[string]any{
			"retentionDays":     7,
			"javaOpts":          "-Xms1g -Xmx1g",
			"allowedNamespaces": []any{"monitoring"},
			"storage":           map[string]any{"className": "gp3", "size": "100Gi", "retainOnDelete": true},
			"resources": map[string]any{
				"requests": map[string]any{"cpu": "1", "memory": "2Gi"},
				"limits":   map[string]any{"cpu": "2", "memory": "4Gi"},
			},
		},
		"fluentd": map[string]any{
			"bufferSize": "2g",
			"resources": map[string]any{
				"requests": map[string]any{"cpu": "100m", "memory": "256Mi"},
				"limits":   map[string]any{"cpu": "1", "memory": "1Gi"},
			},
		},
		"kibana": map[string]any{
			"resources": map[string]any{
				"requests": map[string]any{"cpu": "250m", "memory": "512Mi"},
				"limits":   map[string]any{"cpu": "2", "memory": "2Gi"},
			},
		},
	}
}

func configureDataServiceCatalog(catalog map[string]any) {
	definitions := map[string]struct {
		version, repository, database, mountPath string
		versions                                 []any
		port                                     int
	}{
		"mysql":    {"5.7", "mysql", "app", "/var/lib/mysql", []any{"5.7", "8.0", "8.4"}, 3306},
		"redis":    {"5.0.5", "redis", "", "/data", []any{"5.0.5", "6.2", "7.2", "7.4"}, 6379},
		"activemq": {"5.14.3", "webcenter/activemq", "", "/opt/activemq/data", []any{"5.14.3", "5.15.2"}, 61616},
		"mongodb":  {"6.0", "mongo", "app", "/data/db", []any{"4.4", "5.0", "6.0", "7.0", "8.0"}, 27017},
	}
	for key, definition := range definitions {
		component := catalog[key].(map[string]any)
		component["app_version"] = definition.version
		component["app_versions"] = definition.versions
		component["values"] = map[string]any{
			"engine":  key,
			"image":   map[string]any{"repository": definition.repository, "tag": definition.version, "pullPolicy": "IfNotPresent"},
			"service": map[string]any{"port": definition.port, "targetPort": definition.port},
			"auth":    map[string]any{"username": component["username"], "database": definition.database},
			"storage": map[string]any{"enabled": true, "className": "gp3", "size": "20Gi", "mountPath": definition.mountPath, "retainOnDelete": true},
			"resources": map[string]any{
				"requests": map[string]any{"cpu": "250m", "memory": "512Mi"},
				"limits":   map[string]any{"cpu": "2", "memory": "2Gi"},
			},
			"settings": map[string]any{"maxConnections": 500, "appendOnly": true, "clusterNodeTimeoutMs": 5000},
		}
	}
}

// elasticacheDefaultParameterGroup returns the AWS-managed cluster-mode
// parameter group that matches an ElastiCache engine family. The UI stores the
// selected engine and version separately, so a historical engine switch must
// never leave a Valkey default attached to Redis OSS (or the reverse).
func elasticacheDefaultParameterGroup(engine, version string) string {
	engine = strings.ToLower(strings.TrimSpace(engine))
	parts := strings.Split(strings.TrimSpace(version), ".")
	major := ""
	minor := "0"
	if len(parts) > 0 {
		major = parts[0]
	}
	if len(parts) > 1 {
		minor = parts[1]
	}
	switch engine {
	case "valkey":
		if major == "" {
			major = "8"
		}
		return "default.valkey" + major + ".cluster.on"
	case "redis":
		switch major {
		case "7":
			return "default.redis7.cluster.on"
		case "6":
			return "default.redis6.x.cluster.on"
		case "":
			return "default.redis7.cluster.on"
		default:
			return "default.redis" + major + "." + minor + ".cluster.on"
		}
	default:
		return ""
	}
}

// ApplyDefaults upgrades older environment documents without overwriting any
// user-selected value. This keeps component expansion backward compatible.
func ApplyDefaults(doc Document, project, environmentName string) Document {
	var hadWorkloadSubnetZones, hadDataSubnetZones bool
	var hadWorkloadSubnetType, hadNATGatewayMode bool
	legacyWorkloadSubnetType := "public"
	hadNodeGroups := false
	hadWorkloadScheduling := false
	existingNodeGroupNames := make(map[string]struct{})
	legacyNodeGroupsWithoutSubnetType := make(map[string]bool)
	alertPresetVersion := 0
	existingEnabledWithoutCredentialMode := make(map[string]bool)
	_, hadDataServiceDefaultsVersion := doc["data_service_defaults_version"]
	efkStackDefaultsVersion := intValue(doc["efk_stack_defaults_version"])
	hadElastiCacheNodesPerShard := false
	legacyElastiCacheReplicas := 1
	if network, ok := doc["network"].(map[string]any); ok {
		_, hadWorkloadSubnetZones = network["workload_subnet_zones"]
		_, hadDataSubnetZones = network["data_subnet_zones"]
		_, hadWorkloadSubnetType = network["workload_subnet_type"]
		_, hadNATGatewayMode = network["nat_gateway_mode"]
		if configured := strings.TrimSpace(stringValue(network["workload_subnet_type"])); configured != "" {
			legacyWorkloadSubnetType = configured
		}
	}
	if eks, ok := doc["eks"].(map[string]any); ok {
		_, hadWorkloadScheduling = eks["workload_scheduling"]
		if groups, ok := eks["node_groups"].(map[string]any); ok {
			hadNodeGroups = true
			for name, raw := range groups {
				existingNodeGroupNames[name] = struct{}{}
				if group, ok := raw.(map[string]any); ok {
					_, hasSubnetType := group["subnet_type"]
					legacyNodeGroupsWithoutSubnetType[name] = !hasSubnetType
				}
			}
		}
		// The business pool is the only strongly isolated pool. Gateway and
		// platform workloads remain pinned by labels/selectors, but their nodes
		// intentionally have no taint so cluster system Pods are never stranded
		// during bootstrap, upgrades or capacity recovery.
		NormalizeManagedNodeGroupTaints(doc)
	}
	if alerting, ok := doc["alerting"].(map[string]any); ok {
		alertPresetVersion = intValue(alerting["template_preset_version"])
	}
	if dataServices, ok := doc["data_services"].(map[string]any); ok {
		if elasticache, ok := dataServices["elasticache"].(map[string]any); ok {
			_, hadElastiCacheNodesPerShard = elasticache["nodes_per_shard"]
			if configured, exists := elasticache["replicas_per_node_group"]; exists {
				legacyElastiCacheReplicas = intValue(configured)
			}
		}
		for _, key := range []string{"rds", "aurora"} {
			if service, ok := dataServices[key].(map[string]any); ok {
				_, hasMode := service["credential_management"]
				existingEnabledWithoutCredentialMode[key] = !hasMode && boolValue(service["enabled"])
			}
		}
	}
	defaults := DefaultDocument(project, environmentName)
	mergeMissing(map[string]any(doc), map[string]any(defaults))
	// node_groups is a user-managed keyed collection. Defaults may complete
	// fields on a node group that already exists, but must never resurrect a
	// group the user explicitly removed. Re-adding a removed group can turn a
	// safe Terraform destroy into an unexpected replacement.
	if hadNodeGroups {
		if eks, ok := doc["eks"].(map[string]any); ok {
			if groups, ok := eks["node_groups"].(map[string]any); ok {
				for name := range groups {
					if _, existed := existingNodeGroupNames[name]; !existed {
						delete(groups, name)
					}
				}
			}
		}
	}
	// Older releases modeled database and cache subnets as two extra groups.
	// The current design has exactly three Public and three Private subnets;
	// workload/data switches decide which group a resource uses.
	if network, ok := doc["network"].(map[string]any); ok {
		delete(network, "database_subnets")
		delete(network, "elasticache_subnets")
		zones, zonesOK := network["availability_zones"].([]any)
		if !hadWorkloadSubnetZones && zonesOK {
			network["workload_subnet_zones"] = append([]any(nil), zones...)
		}
		if !hadDataSubnetZones && zonesOK {
			network["data_subnet_zones"] = append([]any(nil), zones...)
		}
		if !hadWorkloadSubnetType {
			network["workload_subnet_type"] = "public"
		}
		if !hadNATGatewayMode {
			network["nat_gateway_mode"] = "when-private"
		}
	}
	if eks, ok := doc["eks"].(map[string]any); ok {
		// Scheduling classes are opt-in for newly created environments. Existing
		// environments must not gain new placement constraints during a normal
		// defaults upgrade because that could move or strand running Pods.
		if !hadWorkloadScheduling {
			eks["workload_scheduling"] = map[string]any{"enabled": false}
		}
		if groups, ok := eks["node_groups"].(map[string]any); ok {
			for name, missing := range legacyNodeGroupsWithoutSubnetType {
				if !missing {
					continue
				}
				if group, ok := groups[name].(map[string]any); ok {
					group["subnet_type"] = legacyWorkloadSubnetType
				}
			}
		}
	}
	// Namespace configuration is intentionally name-only. Terraform reconciles
	// the keys with EKS; quota policy can be added later as a separate concern.
	if namespaces, ok := doc["namespaces"].(map[string]any); ok {
		for name := range namespaces {
			namespaces[name] = map[string]any{}
		}
	}
	if alerting, ok := doc["alerting"].(map[string]any); ok && alertPresetVersion < alertTemplatePresetVersion {
		upgradeAlertTemplatePresets(alerting)
	}
	// AWS no longer permits mq.t3.micro for new RabbitMQ brokers. Migrate the
	// platform's former default so an older saved environment does not fail only
	// after the rest of a long Terraform apply has completed.
	if dataServices, ok := doc["data_services"].(map[string]any); ok {
		for _, key := range []string{"rds", "aurora"} {
			if service, ok := dataServices[key].(map[string]any); ok && existingEnabledWithoutCredentialMode[key] {
				// Running resources from older platform releases used RDS-managed
				// Secrets Manager credentials. Never switch those resources to an
				// explicit password during a defaults-only document upgrade.
				service["credential_management"] = "aws-managed"
			}
		}
		if !hadDataServiceDefaultsVersion {
			// The former platform default enabled TLS even for a disabled
			// node-based cache. Migrate only configurations that have not yet
			// enabled the service; never change a possibly running cache silently.
			if elasticache, ok := dataServices["elasticache"].(map[string]any); ok && !boolValue(elasticache["enabled"]) {
				elasticache["tls_enabled"] = false
			}
		}
		if amazonMQ, ok := dataServices["amazon_mq"].(map[string]any); ok {
			if amazonMQ["host_instance_type"] == "mq.t3.micro" {
				amazonMQ["host_instance_type"] = "mq.m7g.medium"
			}
			if engine, ok := amazonMQ["engine"].(string); ok && strings.EqualFold(engine, "RabbitMQ") {
				amazonMQ["engine"] = "RabbitMQ"
			}
		}
		if elasticache, ok := dataServices["elasticache"].(map[string]any); ok {
			// AWS calls this low-level value ReplicasPerNodeGroup, which excludes
			// the primary node. The UI now uses the much less ambiguous total
			// nodes-per-shard value. Preserve existing capacity during migration,
			// then keep the legacy Terraform field derived instead of allowing two
			// independent values to drift and accidentally multiply cluster size.
			if !hadElastiCacheNodesPerShard {
				elasticache["nodes_per_shard"] = max(1, legacyElastiCacheReplicas+1)
			}
			nodesPerShard := intValue(elasticache["nodes_per_shard"])
			if nodesPerShard >= 1 {
				elasticache["replicas_per_node_group"] = nodesPerShard - 1
			}
			configured := strings.TrimSpace(stringValue(elasticache["parameter_group_name"]))
			// AWS-managed defaults are derived data. Repair empty and stale default
			// values while preserving an explicitly named customer parameter group.
			if configured == "" || strings.HasPrefix(configured, "default.") {
				if expected := elasticacheDefaultParameterGroup(stringValue(elasticache["engine"]), stringValue(elasticache["engine_version"])); expected != "" {
					elasticache["parameter_group_name"] = expected
				}
			}
		}
	}
	doc["data_service_defaults_version"] = 3
	// Jenkins chart 5.8.120 pins configuration-as-code 2031 while resolving a
	// newer git plugin that requires 2036, leaving the controller init container
	// in CrashLoopBackOff. Migrate the former built-in default to the current
	// compatible chart; explicitly selected custom versions remain untouched.
	if components, ok := doc["components"].(map[string]any); ok {
		if catalog, ok := components["catalog"].(map[string]any); ok {
			// Built-in charts are versioned with the platform. Always migrate their
			// metadata so a retry applies storage-safety fixes even when the user did
			// not reopen and save the environment form.
			for _, key := range []string{"mysql", "redis", "activemq", "mongodb"} {
				if component, ok := catalog[key].(map[string]any); ok && component["builtin_chart"] == "data-service" {
					component["chart_version"] = dataServiceChartVersion
				}
			}
			if rabbitMQ, ok := catalog["rabbitmq"].(map[string]any); ok && rabbitMQ["builtin_chart"] == "rabbitmq" {
				rabbitMQ["chart_version"] = rabbitMQChartVersion
			}
			if bytebase, ok := catalog["bytebase"].(map[string]any); ok && bytebase["builtin_chart"] == "bytebase" {
				bytebase["chart_version"] = bytebaseChartVersion
			}
			if workbench, ok := catalog["etcd_workbench"].(map[string]any); ok && workbench["builtin_chart"] == "etcd-workbench" {
				workbench["chart_version"] = etcdWorkbenchChartVersion
				workbench["app_version"] = etcdWorkbenchAppVersion
				if values, ok := workbench["values"].(map[string]any); ok {
					mergeMissing(values, defaultEtcdWorkbenchValues())
				} else {
					workbench["values"] = defaultEtcdWorkbenchValues()
				}
			}
			if jenkins, ok := catalog["jenkins"].(map[string]any); ok && jenkins["chart_version"] == "5.8.120" {
				jenkins["chart_version"] = "5.9.34"
			}
			if prometheus, ok := catalog["prometheus"].(map[string]any); ok {
				if values, ok := prometheus["values"].(map[string]any); ok {
					mergeMissing(values, defaultPrometheusValues())
				} else {
					prometheus["values"] = defaultPrometheusValues()
				}
			}
			if collector, ok := catalog["opentelemetry_collector"].(map[string]any); ok {
				collector["builtin_chart"] = "observability-otel"
				collector["chart_version"] = observabilityOTelChartVersion
				if values, ok := collector["values"].(map[string]any); ok {
					defaults := defaultOpenTelemetryCollectorValues(project, environmentName)
					mergeMissing(values, defaults)
					normalizeOpenTelemetryCollectorStorage(values, project, environmentName)
				} else {
					collector["values"] = defaultOpenTelemetryCollectorValues(project, environmentName)
				}
				if boolValue(collector["enabled"]) {
					for _, dependency := range []string{"prometheus", "loki", "jaeger"} {
						if component, exists := catalog[dependency].(map[string]any); exists {
							component["enabled"] = true
						}
					}
					if values, exists := collector["values"].(map[string]any); exists {
						destinations, _ := values["destinations"].(map[string]any)
						if destinations != nil {
							setOpenTelemetryDestination(destinations, "jaeger", true, catalogServiceEndpoint(catalog, "jaeger", 4317, false))
							if tempo, exists := destinations["tempo"].(map[string]any); exists && boolValue(tempo["enabled"]) {
								if component, exists := catalog["tempo"].(map[string]any); exists {
									component["enabled"] = true
								}
								setOpenTelemetryDestination(destinations, "tempo", true, catalogServiceEndpoint(catalog, "tempo", 4317, false))
							}
							setOpenTelemetryDestination(destinations, "prometheus", true, "")
							setOpenTelemetryDestination(destinations, "loki", true, catalogServiceEndpoint(catalog, "loki", 80, true)+"/otlp")
							elasticsearchEnabled := false
							if current, ok := destinations["elasticsearch"].(map[string]any); ok {
								elasticsearchEnabled = boolValue(current["enabled"])
							}
							if jaeger, ok := catalog["jaeger"].(map[string]any); ok {
								if jaegerValues, ok := jaeger["values"].(map[string]any); ok {
									if storage, ok := jaegerValues["storage"].(map[string]any); ok && stringValue(storage["backend"]) == "elasticsearch" {
										elasticsearchEnabled = true
									}
								}
							}
							if elasticsearch, ok := values["elasticsearch"].(map[string]any); ok {
								elasticsearchEnabled = elasticsearchEnabled || boolValue(elasticsearch["enabled"])
								elasticsearch["enabled"] = elasticsearchEnabled
							}
							setOpenTelemetryDestination(destinations, "elasticsearch", elasticsearchEnabled, otelElasticsearchEndpoint(catalog))
						}
					}
				}
			}
			if jaeger, ok := catalog["jaeger"].(map[string]any); ok {
				jaeger["builtin_chart"] = "jaeger-stack"
				jaeger["chart_version"] = jaegerStackChartVersion
				if values, ok := jaeger["values"].(map[string]any); ok {
					mergeMissing(values, defaultJaegerValues(environmentName))
					if storage, ok := values["storage"].(map[string]any); ok {
						if elasticsearch, ok := storage["elasticsearch"].(map[string]any); ok {
							elasticsearch["endpoint"] = otelElasticsearchEndpoint(catalog)
						}
					}
				} else {
					jaeger["values"] = defaultJaegerValues(environmentName)
				}
			}
			if tempo, ok := catalog["tempo"].(map[string]any); ok {
				tempo["chart_version"] = tempoChartVersion
				if values, ok := tempo["values"].(map[string]any); ok {
					mergeMissing(values, defaultTempoValues(environmentName))
				} else {
					tempo["values"] = defaultTempoValues(environmentName)
				}
			}
			if loki, ok := catalog["loki"].(map[string]any); ok {
				if loki["chart_version"] == "6.37.0" {
					loki["chart_version"] = lokiChartVersion
				}
				if values, ok := loki["values"].(map[string]any); ok {
					mergeMissing(values, defaultLokiValues())
				} else {
					loki["values"] = defaultLokiValues()
				}
				// Loki is delivered as a complete logging experience. It has no
				// end-user query UI of its own, so an enabled Loki always brings the
				// managed Grafana instance with it. Terraform also installs Alloy and
				// provisions the Loki data source for that Grafana instance.
				if boolValue(loki["enabled"]) {
					if prometheus, ok := catalog["prometheus"].(map[string]any); ok {
						prometheus["enabled"] = true
					}
					if namespaces, ok := doc["namespaces"].(map[string]any); ok {
						for _, componentKey := range []string{"loki", "prometheus", "jaeger", "tempo", "opentelemetry_collector"} {
							if component, ok := catalog[componentKey].(map[string]any); ok {
								namespace := strings.TrimSpace(stringValue(component["namespace"]))
								if namespace != "" {
									namespaces[namespace] = map[string]any{}
								}
							}
						}
					}
				}
			}
			if stack, ok := catalog["clickvisual_stack"].(map[string]any); ok && boolValue(stack["enabled"]) {
				if namespaces, ok := doc["namespaces"].(map[string]any); ok {
					if values, ok := stack["values"].(map[string]any); ok {
						namespace := strings.TrimSpace(stringValue(values["namespace"]))
						if namespace == "" {
							namespace = strings.TrimSpace(stringValue(stack["namespace"]))
						}
						if namespace != "" {
							values["namespace"] = namespace
							stack["namespace"] = namespace
							namespaces[namespace] = map[string]any{}
						}
						// Pre-0.2.0 environments stored one namespace per
						// subcomponent. Preserve the old top-level namespaces so
						// no running namespace is implicitly destroyed, but all
						// future chart resources use the single namespace above.
						delete(values, "namespaces")
					}
				}
			}
			if stack, ok := catalog["efk_stack"].(map[string]any); ok {
				if stack["builtin_chart"] == "efk-stack" {
					stack["chart_version"] = efkStackChartVersion
				}
				if boolValue(stack["enabled"]) {
					if namespaces, ok := doc["namespaces"].(map[string]any); ok {
						if values, ok := stack["values"].(map[string]any); ok {
							namespace := strings.TrimSpace(stringValue(values["namespace"]))
							if namespace == "" {
								namespace = strings.TrimSpace(stringValue(stack["namespace"]))
							}
							if namespace != "" {
								values["namespace"] = namespace
								stack["namespace"] = namespace
								namespaces[namespace] = map[string]any{}
							}
							if elasticsearch, ok := values["elasticsearch"].(map[string]any); ok {
								allowedNamespaces := make([]any, 0, 2)
								seen := make(map[string]struct{})
								for _, componentKey := range []string{"prometheus", "opentelemetry_collector", "jaeger"} {
									component, exists := catalog[componentKey].(map[string]any)
									if !exists {
										continue
									}
									candidate := strings.TrimSpace(stringValue(component["namespace"]))
									if candidate == "" {
										continue
									}
									if _, duplicate := seen[candidate]; duplicate {
										continue
									}
									seen[candidate] = struct{}{}
									allowedNamespaces = append(allowedNamespaces, candidate)
								}
								elasticsearch["allowedNamespaces"] = allowedNamespaces
							}
							// Older environments allowed a Heap larger than the
							// Elasticsearch container limit. Repair only documents
							// created before strict validation was introduced; later
							// edits are rejected instead of silently rewritten.
							if efkStackDefaultsVersion < 2 {
								normalizeLegacyEFKHeap(values)
							}
						}
					}
				}
			}
			// Every enabled Helm component needs its target Namespace before
			// Terraform can create the release. Persist the Namespace in the
			// environment document so disabling a component later never removes
			// the shared Namespace or workloads belonging to other components.
			if namespaces, ok := doc["namespaces"].(map[string]any); ok {
				for _, raw := range catalog {
					component, valid := raw.(map[string]any)
					if !valid || !boolValue(component["enabled"]) {
						continue
					}
					namespace := strings.TrimSpace(stringValue(component["namespace"]))
					if namespace != "" {
						namespaces[namespace] = map[string]any{}
					}
				}
			}
			if stack, ok := catalog["clickvisual_stack"].(map[string]any); ok && stack["builtin_chart"] == "clickvisual-stack" {
				stack["chart_version"] = clickVisualStackChartVersion
				// The upstream v1.0.6 GitHub release has no matching Docker Hub
				// image. Migrate only the former broken platform default; preserve
				// every image reference explicitly selected by an operator.
				if values, ok := stack["values"].(map[string]any); ok {
					if images, ok := values["images"].(map[string]any); ok {
						configured := strings.TrimSpace(stringValue(images["clickvisual"]))
						if configured == "clickvisual/clickvisual:v1.0.6" || configured == "clickvisual/clickvisual:1.0.6" {
							images["clickvisual"] = clickVisualImage
						}
					}
				}
			}
		}
		if namespaces, ok := doc["namespaces"].(map[string]any); ok {
			for _, key := range []string{"consul", "etcd"} {
				component, valid := components[key].(map[string]any)
				if !valid || !boolValue(component["enabled"]) {
					continue
				}
				if namespace := strings.TrimSpace(stringValue(component["namespace"])); namespace != "" {
					namespaces[namespace] = map[string]any{}
				}
			}
		}
	}
	doc["efk_stack_defaults_version"] = 2
	// Existing EKS is always treated as shared infrastructure. Older documents
	// may have enabled cluster-level add-on management; force it off so a
	// component deployment or destroy can never modify shared EKS add-ons.
	if target, ok := doc["deployment_target"].(map[string]any); ok && TargetType(doc) == TargetExistingEKS {
		target["manage_addons"] = false
	}
	// The two top-level network switches are the single source of truth. Older
	// documents carried a stale per-service display field that could disagree
	// with the subnet Terraform actually selected.
	if network, ok := doc["network"].(map[string]any); ok {
		if dataServices, ok := doc["data_services"].(map[string]any); ok {
			for _, raw := range dataServices {
				if service, ok := raw.(map[string]any); ok {
					service["network_type"] = network["data_subnet_type"]
				}
			}
		}
	}
	doc["project"] = project
	doc["environment"] = environmentName
	configurePrometheusEnvironmentLabels(doc, project, environmentName)
	return doc
}

// configurePrometheusEnvironmentLabels makes every Alertmanager payload
// traceable back to the platform scope. The cluster label is also injected by
// the relay from the environment document, so alerts created before the next
// Helm reconciliation still render with complete context.
func configurePrometheusEnvironmentLabels(doc Document, project, environmentName string) {
	components, ok := doc["components"].(map[string]any)
	if !ok {
		return
	}
	catalog, ok := components["catalog"].(map[string]any)
	if !ok {
		return
	}
	prometheus, ok := catalog["prometheus"].(map[string]any)
	if !ok {
		return
	}
	values, _ := prometheus["values"].(map[string]any)
	if values == nil {
		values = defaultPrometheusValues()
		prometheus["values"] = values
	}
	prometheusValues, _ := values["prometheus"].(map[string]any)
	if prometheusValues == nil {
		prometheusValues = map[string]any{}
		values["prometheus"] = prometheusValues
	}
	prometheusSpec, _ := prometheusValues["prometheusSpec"].(map[string]any)
	if prometheusSpec == nil {
		prometheusSpec = map[string]any{}
		prometheusValues["prometheusSpec"] = prometheusSpec
	}
	externalLabels, _ := prometheusSpec["externalLabels"].(map[string]any)
	if externalLabels == nil {
		externalLabels = map[string]any{}
		prometheusSpec["externalLabels"] = externalLabels
	}
	externalLabels["project"] = project
	externalLabels["environment"] = environmentName
	externalLabels["cluster"] = ClusterName(doc)
}

func defaultAlertTemplates() []any {
	return []any{
		map[string]any{
			"name": "cluster-control-plane-critical", "event_type": "cluster-control-plane", "severity": "critical", "format": "markdown",
			"title": "{{ .StatusIcon }} {{ .StatusText }}｜EKS 控制面监控",
			"body": "**影响范围**\n" +
				"项目：`{{ .Project }}`　环境：`{{ .Environment }}`\n" +
				"集群：`{{ .Cluster }}`\n" +
				"**告警详情**\n" +
				"级别：**{{ .SeverityText }}**　规则：`{{ .AlertName }}`\n" +
				"名称：{{ .AlertNameText }}\n" +
				"{{ if .MonitorTarget }}目标：`{{ .MonitorTarget }}`\n{{ end }}" +
				"{{ if .Instance }}实例：`{{ .Instance }}`\n{{ end }}" +
				"摘要：{{ .Summary }}\n" +
				"说明：{{ .Description }}\n" +
				"{{ if .OriginalMessage }}原始信息：{{ .OriginalMessage }}\n{{ end }}" +
				"**处理建议**\n" +
				"开始：{{ .StartsAt }}\n" +
				"{{ if .RunbookURL }}文档：[查看排查手册]({{ .RunbookURL }})\n{{ end }}" +
				"建议：{{ .Advice }}\n" +
				"{{ if .RelatedAlerts }}**同组其他告警**\n{{ .RelatedAlerts }}\n{{ end }}" +
				"本组共 {{ .AlertCount }} 条",
		},
		map[string]any{
			"name": "kubernetes-workload-critical", "event_type": "kubernetes-workload", "severity": "critical", "format": "markdown",
			"title": "{{ .StatusIcon }} {{ .StatusText }}｜Kubernetes 工作负载",
			"body": "**影响范围**\n" +
				"项目：`{{ .Project }}`　环境：`{{ .Environment }}`\n" +
				"集群：`{{ .Cluster }}`{{ if .Namespace }}　Namespace：`{{ .Namespace }}`{{ end }}\n" +
				"{{ if or .Workload .Pod .Container .Service }}" +
				"{{ if .Workload }}工作负载：`{{ .Workload }}`\n{{ end }}" +
				"{{ if .Pod }}Pod：`{{ .Pod }}`\n{{ end }}" +
				"{{ if .Container }}容器：`{{ .Container }}`\n{{ end }}" +
				"{{ if .Service }}Service：`{{ .Service }}`\n{{ end }}{{ end }}" +
				"**告警详情**\n" +
				"级别：**{{ .SeverityText }}**　规则：`{{ .AlertName }}`\n" +
				"名称：{{ .AlertNameText }}\n" +
				"{{ if .MonitorTarget }}目标：`{{ .MonitorTarget }}`\n{{ end }}" +
				"摘要：{{ .Summary }}\n" +
				"说明：{{ .Description }}\n" +
				"{{ if .OriginalMessage }}原始信息：{{ .OriginalMessage }}\n{{ end }}" +
				"**处理建议**\n" +
				"开始：{{ .StartsAt }}\n" +
				"{{ if .RunbookURL }}文档：[查看排查手册]({{ .RunbookURL }})\n{{ end }}" +
				"建议：{{ .Advice }}\n" +
				"{{ if .RelatedAlerts }}**同组其他告警**\n{{ .RelatedAlerts }}\n{{ end }}" +
				"本组共 {{ .AlertCount }} 条",
		},
		map[string]any{
			"name": "node-resource-warning", "event_type": "node-resource", "severity": "warning", "format": "markdown",
			"title": "{{ .StatusIcon }} {{ .StatusText }}｜EKS 节点资源",
			"body": "**影响范围**\n" +
				"项目：`{{ .Project }}`　环境：`{{ .Environment }}`\n" +
				"集群：`{{ .Cluster }}`\n" +
				"{{ if .Node }}节点：`{{ .Node }}`\n{{ end }}{{ if .Instance }}实例：`{{ .Instance }}`\n{{ end }}" +
				"**告警详情**\n" +
				"级别：**{{ .SeverityText }}**　规则：`{{ .AlertName }}`\n" +
				"{{ if .CurrentValue }}当前值：**{{ .CurrentValue }}**\n{{ end }}{{ if .Threshold }}阈值：{{ .Threshold }}\n{{ end }}" +
				"摘要：{{ .Summary }}\n说明：{{ .Description }}\n" +
				"{{ if .OriginalMessage }}原始信息：{{ .OriginalMessage }}\n{{ end }}" +
				"**处理建议**\n开始：{{ .StartsAt }}\n建议：{{ .Advice }}",
		},
		map[string]any{
			"name": "deployment-failed-critical", "event_type": "deployment", "severity": "critical", "format": "markdown",
			"title": "{{ .StatusIcon }} {{ .StatusText }}｜自动化部署任务",
			"body": "**影响范围**\n" +
				"项目：`{{ .Project }}`　环境：`{{ .Environment }}`\n集群：`{{ .Cluster }}`\n" +
				"{{ if .Stage }}阶段：{{ .Stage }}\n{{ end }}{{ if .JobID }}任务：`{{ .JobID }}`\n{{ end }}" +
				"**失败详情**\n级别：**{{ .SeverityText }}**　规则：`{{ .AlertName }}`\n" +
				"摘要：{{ .Summary }}\n原因：{{ .Description }}\n" +
				"{{ if .OriginalMessage }}原始错误：{{ .OriginalMessage }}\n{{ end }}" +
				"**处理建议**\n开始：{{ .StartsAt }}\n建议：{{ .Advice }}",
		},
		map[string]any{
			"name": "database-connection-critical", "event_type": "database", "severity": "critical", "format": "markdown",
			"title": "{{ .StatusIcon }} {{ .StatusText }}｜数据库连接",
			"body": "**影响范围**\n" +
				"项目：`{{ .Project }}`　环境：`{{ .Environment }}`\n集群：`{{ .Cluster }}`\n" +
				"{{ if .Service }}服务：`{{ .Service }}`\n{{ end }}{{ if .Engine }}引擎：`{{ .Engine }}`\n{{ end }}" +
				"**告警详情**\n级别：**{{ .SeverityText }}**　规则：`{{ .AlertName }}`\n" +
				"{{ if .Duration }}持续：{{ .Duration }}\n{{ end }}摘要：{{ .Summary }}\n说明：{{ .Description }}\n" +
				"{{ if .OriginalMessage }}原始信息：{{ .OriginalMessage }}\n{{ end }}" +
				"**处理建议**\n开始：{{ .StartsAt }}\n建议：{{ .Advice }}",
		},
		map[string]any{
			"name": "service-availability-warning", "event_type": "service-availability", "severity": "warning", "format": "markdown",
			"title": "{{ .StatusIcon }} {{ .StatusText }}｜服务可用性",
			"body": "**影响范围**\n" +
				"项目：`{{ .Project }}`　环境：`{{ .Environment }}`\n集群：`{{ .Cluster }}`\n" +
				"{{ if .Service }}服务：`{{ .Service }}`\n{{ end }}" +
				"**告警详情**\n级别：**{{ .SeverityText }}**　规则：`{{ .AlertName }}`\n" +
				"{{ if .HTTPStatus }}HTTP：`{{ .HTTPStatus }}`\n{{ end }}{{ if .Availability }}可用率：**{{ .Availability }}**\n{{ end }}" +
				"摘要：{{ .Summary }}\n说明：{{ .Description }}\n" +
				"{{ if .OriginalMessage }}原始信息：{{ .OriginalMessage }}\n{{ end }}" +
				"**处理建议**\n开始：{{ .StartsAt }}\n建议：{{ .Advice }}",
		},
	}
}

func upgradeAlertTemplatePresets(alerting map[string]any) {
	presets := defaultAlertTemplates()
	presetNames := make(map[string]struct{}, len(presets))
	for _, raw := range presets {
		if item, ok := raw.(map[string]any); ok {
			presetNames[stringValue(item["name"])] = struct{}{}
		}
	}
	result := append([]any(nil), presets...)
	if current, ok := alerting["templates"].([]any); ok {
		for _, raw := range current {
			item, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if _, bundled := presetNames[stringValue(item["name"])]; bundled {
				continue
			}
			result = append(result, raw)
		}
	}
	alerting["templates"] = result
	alerting["template_preset_version"] = alertTemplatePresetVersion
}

func defaultPrometheusValues() map[string]any {
	return map[string]any{
		// EKS owns these control-plane processes and does not expose their
		// metrics endpoints to tenant clusters. Enabling the upstream monitors
		// creates permanent *Down alerts even when the EKS control plane is
		// healthy, so keep them disabled for platform-managed EKS environments.
		"kubeControllerManager": map[string]any{"enabled": false},
		"kubeScheduler":         map[string]any{"enabled": false},
		"kubeEtcd":              map[string]any{"enabled": false},
		"additionalPrometheusRulesMap": map[string]any{
			"ops-deploy-core": map[string]any{
				"groups": []any{
					map[string]any{"name": "ops-deploy-kubernetes-workloads", "rules": []any{
						map[string]any{"alert": "PodCrashLoopBackOff", "expr": "max_over_time(kube_pod_container_status_waiting_reason{reason=\"CrashLoopBackOff\"}[5m]) >= 1", "for": "5m", "labels": map[string]any{"severity": "critical"}, "annotations": map[string]any{"summary": "Pod 持续 CrashLoopBackOff", "description": "{{ $labels.namespace }}/{{ $labels.pod }} 容器 {{ $labels.container }} 持续重启。"}},
						map[string]any{"alert": "PodRestartTooOften", "expr": "increase(kube_pod_container_status_restarts_total[15m]) > 5", "for": "5m", "labels": map[string]any{"severity": "warning"}, "annotations": map[string]any{"summary": "Pod 重启次数过多", "description": "{{ $labels.namespace }}/{{ $labels.pod }} 15 分钟内重启超过 5 次。"}},
						map[string]any{"alert": "DeploymentReplicasMismatch", "expr": "kube_deployment_spec_replicas > kube_deployment_status_replicas_available", "for": "10m", "labels": map[string]any{"severity": "critical"}, "annotations": map[string]any{"summary": "Deployment 可用副本不足", "description": "{{ $labels.namespace }}/{{ $labels.deployment }} 实际可用副本低于期望值。"}},
					}},
					map[string]any{"name": "ops-deploy-node-storage", "rules": []any{
						map[string]any{"alert": "NodeCPUHigh", "expr": "(1 - avg by(instance) (rate(node_cpu_seconds_total{mode=\"idle\"}[5m]))) > 0.85", "for": "10m", "labels": map[string]any{"severity": "warning"}, "annotations": map[string]any{"summary": "节点 CPU 使用率过高", "description": "节点 {{ $labels.instance }} CPU 使用率持续超过 85%。"}},
						map[string]any{"alert": "NodeMemoryHigh", "expr": "(1 - node_memory_MemAvailable_bytes / node_memory_MemTotal_bytes) > 0.90", "for": "10m", "labels": map[string]any{"severity": "warning"}, "annotations": map[string]any{"summary": "节点内存使用率过高", "description": "节点 {{ $labels.instance }} 内存使用率持续超过 90%。"}},
						map[string]any{"alert": "PVCUsageHigh", "expr": "kubelet_volume_stats_used_bytes / kubelet_volume_stats_capacity_bytes > 0.85", "for": "10m", "labels": map[string]any{"severity": "warning"}, "annotations": map[string]any{"summary": "PVC 使用率过高", "description": "{{ $labels.namespace }}/{{ $labels.persistentvolumeclaim }} 使用率持续超过 85%。"}},
					}},
				},
			},
		},
		"prometheus": map[string]any{
			"prometheusSpec": map[string]any{
				"retention":                 "15d",
				"enableRemoteWriteReceiver": true,
				"storageSpec": map[string]any{"volumeClaimTemplate": map[string]any{
					"spec": map[string]any{"storageClassName": "gp3", "accessModes": []any{"ReadWriteOnce"}, "resources": map[string]any{"requests": map[string]any{"storage": "50Gi"}}},
				}},
			},
		},
		"alertmanager": map[string]any{
			"alertmanagerSpec": map[string]any{
				"storage": map[string]any{"volumeClaimTemplate": map[string]any{
					"spec": map[string]any{"storageClassName": "gp3", "accessModes": []any{"ReadWriteOnce"}, "resources": map[string]any{"requests": map[string]any{"storage": "10Gi"}}},
				}},
			},
		},
		"grafana": map[string]any{
			"defaultDashboardsEnabled":  true,
			"defaultDashboardsTimezone": "browser",
			// Grafana uses a ReadWriteOnce EBS volume. RollingUpdate creates the
			// replacement Pod before the old Pod releases that volume and can sit
			// in Multi-Attach until the Helm timeout. Recreate gives a short,
			// deterministic restart while preserving the PVC and dashboards.
			"deploymentStrategy": map[string]any{"type": "Recreate"},
			"persistence": map[string]any{
				"enabled": true, "storageClassName": "gp3", "size": "10Gi", "accessModes": []any{"ReadWriteOnce"},
			},
			"sidecar": map[string]any{
				"dashboards": map[string]any{
					"enabled":         true,
					"label":           "grafana_dashboard",
					"labelValue":      "1",
					"searchNamespace": "ALL",
				},
				"datasources": map[string]any{
					"enabled":                             true,
					"defaultDatasourceEnabled":            true,
					"isDefaultDatasource":                 true,
					"name":                                "Prometheus",
					"uid":                                 "prometheus",
					"searchNamespace":                     "ALL",
					"resource":                            "both",
					"createPrometheusReplicasDatasources": false,
				},
			},
		},
	}
}

func defaultOpenTelemetryCollectorValues(project, environmentName string) map[string]any {
	return map[string]any{
		"fullnameOverride": "opentelemetry-collector",
		"project":          project,
		"environment":      environmentName,
		"clusterName":      project + "-" + environmentName + "-eks",
		"replicaCount":     1,
		"image": map[string]any{
			"repository": "ghcr.io/open-telemetry/opentelemetry-collector-releases/opentelemetry-collector-contrib",
			"tag":        "0.158.0",
			"pullPolicy": "IfNotPresent",
		},
		"resources": map[string]any{
			"requests": map[string]any{"cpu": "200m", "memory": "256Mi"},
			"limits":   map[string]any{"cpu": "1", "memory": "1Gi"},
		},
		"storage": map[string]any{
			"enabled":        true,
			"className":      "gp3",
			"initialSize":    "10Gi",
			"expandedSize":   "",
			"queueSize":      1000,
			"retainOnDelete": true,
		},
		"destinations": map[string]any{
			"jaeger":     map[string]any{"enabled": true, "endpoint": "jaeger.monitoring.svc.cluster.local:4317"},
			"tempo":      map[string]any{"enabled": false, "endpoint": "tempo.monitoring.svc.cluster.local:4317"},
			"prometheus": map[string]any{"enabled": true},
			"loki":       map[string]any{"enabled": true, "endpoint": "http://loki-gateway.monitoring.svc.cluster.local/otlp"},
			"elasticsearch": map[string]any{
				"enabled": false, "endpoint": "http://otel-elasticsearch.monitoring.svc.cluster.local:9200", "secretName": "otel-elasticsearch-access", "usernameKey": "username", "passwordKey": "password",
			},
		},
		"elasticsearch": defaultOpenTelemetryElasticsearchValues(environmentName),
		"agent": map[string]any{
			"enabled":   true,
			"logs":      map[string]any{"enabled": true, "includeNamespaces": []any{}, "excludeNamespaces": []any{"monitoring"}, "includeServices": []any{}, "excludeServices": []any{}},
			"metrics":   map[string]any{"enabled": true},
			"resources": map[string]any{"requests": map[string]any{"cpu": "100m", "memory": "192Mi"}, "limits": map[string]any{"cpu": "500m", "memory": "512Mi"}},
		},
		"serviceMonitor": map[string]any{"enabled": true, "labels": map[string]any{"release": "prometheus"}},
	}
}

func defaultOpenTelemetryElasticsearchValues(environmentName string) map[string]any {
	size := "50Gi"
	javaOpts := "-Xms1g -Xmx1g"
	resources := map[string]any{
		"requests": map[string]any{"cpu": "500m", "memory": "2Gi"},
		"limits":   map[string]any{"cpu": "2", "memory": "4Gi"},
	}
	if environmentName == "prod" {
		size = "200Gi"
		javaOpts = "-Xms2g -Xmx2g"
		resources = map[string]any{
			"requests": map[string]any{"cpu": "1", "memory": "4Gi"},
			"limits":   map[string]any{"cpu": "4", "memory": "8Gi"},
		}
	}
	return map[string]any{
		"enabled":  false,
		"mode":     "standalone",
		"replicas": 1,
		"image": map[string]any{
			"repository": "docker.elastic.co/elasticsearch/elasticsearch",
			"tag":        "8.19.17",
			"pullPolicy": "IfNotPresent",
		},
		"auth":     map[string]any{"username": "elastic"},
		"javaOpts": javaOpts,
		"storage": map[string]any{
			"className":      "gp3",
			"initialSize":    size,
			"expandedSize":   "",
			"retainOnDelete": true,
		},
		"resources": resources,
		"timeout":   1200,
	}
}

func defaultJaegerValues(environmentName string) map[string]any {
	size := "20Gi"
	retention := "168h"
	if environmentName == "prod" {
		size = "100Gi"
		retention = "720h"
	}
	return map[string]any{
		"fullnameOverride": "jaeger",
		"replicaCount":     1,
		"image": map[string]any{
			"repository": "jaegertracing/jaeger", "tag": "2.20.0", "pullPolicy": "IfNotPresent",
		},
		"basicAuth": map[string]any{"enabled": true, "username": "admin"},
		"storage": map[string]any{
			"backend": "badger", "className": "gp3", "initialSize": size, "expandedSize": "",
			"retention": retention, "retainOnDelete": true,
			"elasticsearch": map[string]any{
				"endpoint": "http://otel-elasticsearch.monitoring.svc.cluster.local:9200", "username": "elastic",
				"indexPrefix": "jaeger", "shards": 1, "replicas": 0, "retentionDays": 30,
				"indexCleaner": map[string]any{
					"enabled": true, "schedule": "17 2 * * *",
					"image": map[string]any{
						"repository": "cr.jaegertracing.io/jaegertracing/jaeger-es-index-cleaner", "tag": "2.20.0", "pullPolicy": "IfNotPresent",
					},
				},
			},
		},
		"resources": map[string]any{
			"requests": map[string]any{"cpu": "250m", "memory": "512Mi"},
			"limits":   map[string]any{"cpu": "2", "memory": "2Gi"},
		},
		"serviceMonitor": map[string]any{"enabled": true, "labels": map[string]any{"release": "prometheus"}},
	}
}

func defaultTempoValues(environmentName string) map[string]any {
	retention := "168h"
	size := "20Gi"
	if environmentName == "prod" {
		retention = "720h"
		size = "100Gi"
	}
	return map[string]any{
		"fullnameOverride": "tempo",
		"replicas":         1,
		"tempo": map[string]any{
			"reportingEnabled": false,
			"retention":        retention,
			"metricsGenerator": map[string]any{
				"enabled":        true,
				"remoteWriteUrl": "http://prometheus-kube-prometheus-prometheus.monitoring.svc.cluster.local:9090/api/v1/write",
				"processor":      map[string]any{"service_graphs": map[string]any{}, "span_metrics": map[string]any{"enable_target_info": true}},
			},
			"overrides": map[string]any{"defaults": map[string]any{"metrics_generator": map[string]any{"processors": []any{"service-graphs", "span-metrics"}}}},
			"resources": map[string]any{"requests": map[string]any{"cpu": "250m", "memory": "512Mi"}, "limits": map[string]any{"cpu": "2", "memory": "2Gi"}},
			"storage":   map[string]any{"trace": map[string]any{"backend": "local", "local": map[string]any{"path": "/var/tempo/traces"}, "wal": map[string]any{"path": "/var/tempo/wal"}}},
		},
		"persistence":    map[string]any{"enabled": true, "storageClassName": "gp3", "accessModes": []any{"ReadWriteOnce"}, "size": size},
		"serviceMonitor": map[string]any{"enabled": true, "additionalLabels": map[string]any{"release": "prometheus"}},
	}
}

func normalizeOpenTelemetryCollectorStorage(values map[string]any, project, environmentName string) {
	storage, _ := values["storage"].(map[string]any)
	if storage == nil {
		return
	}
	values["project"] = project
	values["environment"] = environmentName
	// Remove keys from the former upstream Collector chart. The platform now
	// owns a small built-in Helm chart that renders one node Agent DaemonSet and
	// one persistent Gateway StatefulSet. Keeping obsolete upstream keys makes
	// the UI misleading even though the built-in chart ignores them.
	for _, key := range []string{"mode", "presets", "config", "extraVolumeMounts", "statefulset", "podSecurityContext", "service", "podDisruptionBudget"} {
		delete(values, key)
	}
	image, _ := values["image"].(map[string]any)
	if image == nil {
		image = map[string]any{}
		values["image"] = image
	}
	image["repository"] = "ghcr.io/open-telemetry/opentelemetry-collector-releases/opentelemetry-collector-contrib"
	image["tag"] = "0.158.0"
	image["pullPolicy"] = "IfNotPresent"
	if elasticsearch, ok := values["elasticsearch"].(map[string]any); ok {
		if elasticsearchStorage, ok := elasticsearch["storage"].(map[string]any); ok {
			// Migrate the first local implementation which exposed one mutable
			// `size` value. The initial claim template stays immutable; all later
			// expansion is recorded separately and reconciled directly onto PVCs.
			if legacySize := strings.TrimSpace(stringValue(elasticsearchStorage["size"])); legacySize != "" {
				elasticsearchStorage["initialSize"] = legacySize
				delete(elasticsearchStorage, "size")
			}
		}
	}
}

func setOpenTelemetryDestination(destinations map[string]any, name string, enabled bool, endpoint string) {
	destination, _ := destinations[name].(map[string]any)
	if destination == nil {
		destination = map[string]any{}
		destinations[name] = destination
	}
	destination["enabled"] = enabled
	if endpoint != "" {
		destination["endpoint"] = endpoint
	}
}

func catalogServiceEndpoint(catalog map[string]any, key string, fallbackPort int, http bool) string {
	component, _ := catalog[key].(map[string]any)
	namespace := strings.TrimSpace(stringValue(component["namespace"]))
	if namespace == "" {
		namespace = "monitoring"
	}
	service := strings.TrimSpace(stringValue(component["service_name"]))
	if service == "" {
		service = key
	}
	port := intValue(component["service_port"])
	if port <= 0 {
		port = fallbackPort
	}
	endpoint := service + "." + namespace + ".svc.cluster.local:" + strconv.Itoa(port)
	if http {
		return "http://" + endpoint
	}
	return endpoint
}

func otelElasticsearchEndpoint(catalog map[string]any) string {
	component, _ := catalog["opentelemetry_collector"].(map[string]any)
	namespace := strings.TrimSpace(stringValue(component["namespace"]))
	if namespace == "" {
		namespace = "monitoring"
	}
	return "http://otel-elasticsearch." + namespace + ".svc.cluster.local:9200"
}

func defaultLokiValues() map[string]any {
	return map[string]any{
		"deploymentMode": "SingleBinary",
		"loki": map[string]any{
			"auth_enabled":  false,
			"commonConfig":  map[string]any{"replication_factor": 1},
			"storage":       map[string]any{"type": "filesystem"},
			"useTestSchema": true,
			"limits_config": map[string]any{
				"retention_period":        "168h",
				"ingestion_rate_mb":       16,
				"ingestion_burst_size_mb": 32,
			},
			"ingester": map[string]any{
				"concurrent_flushes": 4,
				"wal": map[string]any{
					"replay_memory_ceiling": "256MB",
				},
			},
			"pattern_ingester": map[string]any{"enabled": false},
			"compactor": map[string]any{
				"retention_enabled":    true,
				"delete_request_store": "filesystem",
			},
		},
		"singleBinary": map[string]any{
			"replicas": 1,
			"extraEnv": []any{
				map[string]any{"name": "GOMEMLIMIT", "value": "2800MiB"},
				map[string]any{"name": "GOGC", "value": "50"},
			},
			"resources": map[string]any{
				"requests": map[string]any{"cpu": "500m", "memory": "1Gi"},
				"limits":   map[string]any{"cpu": "2", "memory": "5Gi"},
			},
			"persistence": map[string]any{
				"enabled": true, "storageClass": "gp3", "size": "20Gi",
				"enableStatefulSetAutoDeletePVC": true,
			},
		},
		"memberlist": map[string]any{
			"service": map[string]any{"publishNotReadyAddresses": true},
		},
		"read":         map[string]any{"replicas": 0},
		"write":        map[string]any{"replicas": 0},
		"backend":      map[string]any{"replicas": 0},
		"chunksCache":  map[string]any{"enabled": false},
		"resultsCache": map[string]any{"enabled": false},
	}
}

func mergeMissing(target, defaults map[string]any) {
	for key, fallback := range defaults {
		current, exists := target[key]
		if !exists || current == nil {
			target[key] = fallback
			continue
		}
		currentMap, currentOK := current.(map[string]any)
		fallbackMap, fallbackOK := fallback.(map[string]any)
		if currentOK && fallbackOK {
			mergeMissing(currentMap, fallbackMap)
		}
	}
}

func catalogComponent(displayName, category, repository, chart, version, serviceName string, servicePort int, protocol, username, secretName, secretKey string) map[string]any {
	return map[string]any{
		"enabled": false, "display_name": displayName, "category": category,
		"repository": repository, "chart": chart, "chart_version": version,
		"deployment_mode": "standalone", "replicas": 1, "replica_paths": []any{},
		"release_name": releaseNameFor(serviceName), "namespace": defaultNamespace(category),
		"service_name": serviceName, "service_port": servicePort, "protocol": protocol,
		"username": username, "secret_name": secretName, "secret_key": secretKey,
		"domain": "", "tls": false, "timeout": 1200, "values": map[string]any{},
	}
}

func releaseNameFor(service string) string {
	switch service {
	case "argocd-server":
		return "argocd"
	case "gitlab-webservice-default":
		return "gitlab"
	case "prometheus-kube-prometheus-prometheus":
		return "prometheus"
	case "prometheus-grafana":
		return "prometheus"
	case "loki-gateway":
		return "loki"
	case "higress-gateway":
		return "higress"
	case "higress-console":
		return "higress"
	case "nginx-ingress-controller":
		return "nginx-ingress"
	case "nacos-cs":
		return "nacos"
	case "xxl-job":
		return "xxl-job"
	case "tekton-dashboard":
		return "tekton"
	default:
		return service
	}
}

func defaultNamespace(category string) string {
	switch category {
	case "监控", "日志":
		return "monitoring"
	default:
		return "platform-server"
	}
}
