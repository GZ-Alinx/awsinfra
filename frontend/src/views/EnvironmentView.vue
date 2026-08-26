<template>
  <div v-if="form.network">
    <div class="page-header"><div><h2>{{ store.currentProject?.display_name }} / {{ store.currentEnvironment?.display_name }} 部署配置</h2><p>{{ existingEKSTarget ? '当前环境接入已有 EKS，保存并通过接入检查后可直接开始或更新阶段二。' : '所有参数都可以在部署前调整；一个主按钮会在同一任务内生成计划并执行，并自动识别首次部署或更新部署。' }}</p></div><a-space><a-tag v-if="existingEKSTarget" color="purple">已有 EKS</a-tag><a-tag v-if="dirty" color="orange">有未保存修改</a-tag><a-tag v-else color="green">配置已保存</a-tag></a-space></div>
	    <a-alert v-if="!store.canConfigure" type="warning" show-icon class="full-card">当前用户只有查看权限，不能修改部署配置。</a-alert>
	    <a-alert v-if="!awsCredentialReady" type="warning" show-icon class="full-card">
	      当前项目未绑定并选中自己的 AWS 凭据。AWS 查询、状态采集、部署和销毁均已禁用；平台绝不会使用其他项目凭据、AWS Profile、IAM Role 或默认凭据链。
	      <template #action><a-button size="small" @click="router.push({ name: 'aws-connection' })">配置 AWS 连接</a-button></template>
	    </a-alert>
	    <a-alert v-else-if="cloudSync" :type="cloudSyncAlertType" show-icon class="full-card">
	      <template #title>{{ cloudSyncTitle }}</template>
	      {{ cloudSyncDescription }} 可追加列表（如白名单和仓库）采用增量合并；规格、版本、容量等单值发现 AWS 外部修改时会停止部署，不会静默还原。
	      <template #action>
	        <a-space>
	          <a-button size="small" :loading="store.loadingResources" :disabled="environmentBusy" @click="refreshAWSConfiguration"><icon-refresh />读取 AWS 实际配置</a-button>
	          <a-popconfirm v-if="cloudSync.blocking_changes || cloudSync.pending_fields" content="将以 AWS 当前实际参数覆盖平台已保存的云资源参数；不会修改 AWS 资源。确认继续？" @ok="syncAWSConfiguration">
	            <a-button size="small" type="primary" :loading="syncingAWSConfiguration" :disabled="!store.canConfigure || dirty || environmentBusy">采用 AWS 实际配置</a-button>
	          </a-popconfirm>
	          <a-button v-if="cloudSync.blocking_changes" size="small" @click="router.push({ name: 'resources' })">查看参数差异</a-button>
	        </a-space>
	      </template>
	    </a-alert>
	    <a-alert v-for="warning in cloudSyncWarnings" :key="warning" type="error" show-icon class="full-card">{{ warning }}</a-alert>
	    <a-card v-if="existingEKSTarget" class="phase-card full-card">
	      <a-steps :current="baseReady ? 2 : 1" type="arrow">
	        <a-step title="已有 EKS 接入检查" :description="`${form.region} · ${form.deployment_target?.cluster_name || '未选择集群'}`" />
	        <a-step title="组件与接入部署" description="Namespace、基础服务、可选组件、日志监控、TLS、域名与告警" />
	      </a-steps>
	      <a-alert v-if="!baseReady" type="warning" show-icon style="margin-top:12px">需要集群为 ACTIVE，且当前项目 AWS 身份拥有 Kubernetes 管理权限。保存配置会自动校验，也可以刷新接入状态。</a-alert>
	      <a-alert v-else type="success" show-icon style="margin-top:12px">已有 EKS 接入正常，可以直接执行组件与接入部署。</a-alert>
	    </a-card>
	    <a-card v-else class="phase-card deployment-phase-card full-card" :class="`phase-${currentStage}-active`">
	      <a-steps :current="currentStage" type="arrow">
	        <a-step title="阶段 1 · 基础资源与服务" description="VPC、EKS、云中间件与云数据库、ECR、必要 Add-on、Consul/etcd" />
	        <a-step title="阶段 2 · 组件与接入配置" description="可选组件、日志监控、TLS证书、域名路由和告警配置" />
	      </a-steps>
	      <a-alert v-if="!baseReady" type="info" show-icon style="margin-top:12px">阶段 2 参数可以提前填写和保存，但只有 EKS 达到 ACTIVE 后才能执行，不影响阶段 1 创建基础资源。</a-alert>
	      <a-alert v-else type="success" show-icon style="margin-top:12px">EKS 已就绪，可以执行阶段 2 组件、证书、域名和可观测性配置。</a-alert>
	    </a-card>
    <a-tabs v-model:active-key="activeTab" class="deployment-stage-tabs" direction="horizontal" position="top" type="rounded" lazy-load destroy-on-hide @change="handleTabChange">
	      <a-tab-pane key="basic">
          <template #title><span class="deployment-stage-tab-title"><span class="deployment-stage-badge" :class="existingEKSTarget ? 'existing' : 'phase-one'">{{ existingEKSTarget ? 'EKS' : '1' }}</span><span>{{ existingEKSTarget ? '环境与 Namespace' : '基础信息' }}</span></span></template>
        <a-card class="form-card"><template #title><span class="card-title">项目与区域</span></template>
          <a-form :model="form" layout="vertical"><a-grid :cols="3" :col-gap="16">
            <a-grid-item><a-form-item label="项目"><a-input :model-value="store.currentProject?.display_name" disabled /></a-form-item></a-grid-item>
            <a-grid-item><a-form-item label="环境"><a-input :model-value="store.currentEnvironment?.display_name" disabled /></a-form-item></a-grid-item>
	            <a-grid-item><a-form-item label="AWS Region" required><a-select v-model="form.region" allow-search :disabled="existingEKSTarget" @change="changeRegion"><a-option v-for="region in store.platform?.aws_regions || []" :key="region.code" :value="region.code">{{ region.code }} · {{ region.name }}{{ region.opt_in ? '（需启用）' : '' }}</a-option></a-select></a-form-item></a-grid-item>
	            <a-grid-item><a-form-item label="负责人（Owner）" extra="标识资源责任人或负责团队，用于资源追踪、审计和故障通知。"><a-input v-model="form.tags.Owner" /></a-form-item></a-grid-item>
	            <a-grid-item><a-form-item label="成本中心（Cost Center）" extra="用于 AWS 成本分摊、预算统计和账单标签，例如业务线或部门编码。"><a-input v-model="form.tags.CostCenter" /></a-form-item></a-grid-item>
	            <a-grid-item v-if="!existingEKSTarget"><a-form-item label="Kubernetes Service CIDR"><a-input v-model="form.network.service_ipv4_cidr" /></a-form-item></a-grid-item>
	          </a-grid></a-form>
	        </a-card>
	        <a-card v-if="existingEKSTarget" class="form-card"><template #title><span class="card-title">已有 EKS 接入</span></template><template #extra><a-button :loading="store.loadingStatus" :disabled="dirty" @click="store.loadStatus(true)"><icon-refresh />刷新接入状态</a-button></template>
	          <a-alert type="warning" show-icon class="full-card">该环境不会创建或接管 EKS、VPC、节点组和 EKS Add-on。平台只卸载本环境管理的组件与接入资源，Namespace 永久保留，不会删除其中其他工作负载。</a-alert>
	          <a-form :model="form.deployment_target" layout="vertical"><a-grid :cols="3" :col-gap="16">
	            <a-grid-item><a-form-item label="部署模式"><a-input model-value="接入已有 EKS（跳过阶段1）" disabled /></a-form-item></a-grid-item>
	            <a-grid-item><a-form-item label="EKS 集群名称" required extra="为保证 Terraform 状态不会指向另一集群，创建后不可修改。"><a-input v-model="form.deployment_target.cluster_name" disabled /></a-form-item></a-grid-item>
	            <a-grid-item><a-form-item label="共享集群保护" extra="固定开启。平台不会创建、升级或删除已有 EKS 的 Add-on、节点组、VPC、StorageClass 和集群级 IAM 对接。"><a-input model-value="已开启（不可关闭）" disabled /></a-form-item></a-grid-item>
	          </a-grid></a-form>
	        </a-card>
	        <a-card><template #title><span class="card-title">Namespaces</span></template><template #extra><a-button type="primary" size="small" :disabled="!store.canConfigure" @click="namespaceVisible = true"><icon-plus />添加 Namespace</a-button></template>
	          <a-alert type="success" show-icon class="full-card">
	            这里只显示人工创建的 Namespace，新环境默认为空。platform-server 等组件运行 Namespace 按启用组件自动创建并永久保留，不在此处重复展示。
	          </a-alert>
	          <a-table :data="namespaceRows" :pagination="false" size="small"><template #columns><a-table-column title="Namespace" data-index="name" /><a-table-column title="同步策略"><template #cell><a-tag color="arcoblue">{{ existingEKSTarget ? '组件部署同步' : '阶段1自动同步' }}</a-tag></template></a-table-column><a-table-column title="安全状态" :width="180"><template #cell><a-tag color="green">永久禁止删除</a-tag></template></a-table-column></template><template #empty><a-empty description="尚未配置 Namespace" /></template></a-table>
	        </a-card>
      </a-tab-pane>

	      <a-tab-pane v-if="!existingEKSTarget" key="network">
          <template #title><span class="deployment-stage-tab-title"><span class="deployment-stage-badge phase-one">1</span><span>网络规划</span></span></template>
        <a-card><template #title><span class="card-title">VPC 网络来源</span></template><template #extra><a-button v-if="form.network.mode === 'create'" size="small" @click="resetRegionNetwork">按当前区域重建 3AZ</a-button><a-button v-else size="small" :loading="loadingVPCs" @click="loadVPCs(true)"><icon-refresh />刷新 AWS VPC</a-button></template>
          <a-form :model="form" layout="vertical"><a-form-item label="网络模式" :extra="baseReady ? 'EKS 已创建，VPC 不可变更；如需换 VPC 请新建环境。' : '创建 EKS 前可选择新建或复用已有 VPC。'"><a-radio-group v-model="form.network.mode" type="button" :disabled="baseReady" @change="changeNetworkMode"><a-radio value="create">新建 VPC</a-radio><a-radio value="existing">使用已有 VPC</a-radio></a-radio-group></a-form-item></a-form>
          <template v-if="form.network.mode === 'create'">
            <a-alert type="info" show-icon class="full-card">创建 3 个 Public 与 3 个 Private 子网。NAT 可按 Private 网络需求自动创建，也可提前创建供后续 Private 工作负载使用；托管数据库与中间件始终只提供 VPC 私网地址。</a-alert>
            <a-form :model="form" layout="vertical"><a-grid :cols="4" :col-gap="16">
              <a-grid-item><a-form-item label="VPC CIDR"><a-input v-model="form.network.vpc_cidr" class="cidr-input" /></a-form-item></a-grid-item>
              <a-grid-item><a-form-item label="EKS/组件网络"><a-radio-group v-model="form.network.workload_subnet_type" type="button"><a-radio value="public">Public</a-radio><a-radio value="private">Private</a-radio></a-radio-group></a-form-item></a-grid-item>
	              <a-grid-item><a-form-item label="云中间件与云数据库网络"><a-radio-group v-model="form.network.data_subnet_type" type="button"><a-radio value="public">Public</a-radio><a-radio value="private">Private</a-radio></a-radio-group></a-form-item></a-grid-item>
	              <a-grid-item><a-form-item label="NAT Gateway 策略" :extra="natGatewayHint"><a-select v-model="form.network.nat_gateway_mode"><a-option value="when-private">Private 网络按需创建</a-option><a-option value="always">始终创建</a-option><a-option value="disabled">不创建</a-option></a-select></a-form-item></a-grid-item>
	              <a-grid-item><a-form-item label="NAT 高可用" :extra="form.network.single_nat_gateway ? '单 NAT 成本较低；发生 AZ 故障时 Private 出口会受影响。' : '每个 AZ 创建一个 NAT，跨 AZ 高可用但费用更高。'"><a-switch v-model="form.network.single_nat_gateway" :disabled="form.network.nat_gateway_mode === 'disabled'" checked-text="单 NAT" unchecked-text="三 AZ" /></a-form-item></a-grid-item>
	              <a-grid-item :span="2"><a-form-item :label="`EKS/组件使用的 ${form.network.workload_subnet_type === 'private' ? 'Private' : 'Public'} 子网`"><a-select v-model="form.network.workload_subnet_zones" multiple><a-option v-for="zone in form.network.availability_zones" :key="zone" :value="zone">{{ zone }} · {{ form.network.workload_subnet_type === 'private' ? form.network.private_subnets[zone] : form.network.public_subnets[zone] }}</a-option></a-select></a-form-item></a-grid-item>
	              <a-grid-item :span="2"><a-form-item :label="`云服务使用的 ${form.network.data_subnet_type === 'private' ? 'Private' : 'Public'} 子网`"><a-select v-model="form.network.data_subnet_zones" multiple><a-option v-for="zone in form.network.availability_zones" :key="zone" :value="zone">{{ zone }} · {{ form.network.data_subnet_type === 'private' ? form.network.private_subnets[zone] : form.network.public_subnets[zone] }}</a-option></a-select></a-form-item></a-grid-item>
	            </a-grid></a-form>
	            <a-table :data="subnetRows" :pagination="false" size="small" class="subnet-table"><template #columns><a-table-column title="Availability Zone" data-index="zone" :width="190" /><a-table-column title="Public 子网" :width="230"><template #cell="{ record }"><a-input v-model="form.network.public_subnets[record.zone]" class="cidr-input" /></template></a-table-column><a-table-column title="Private 子网" :width="230"><template #cell="{ record }"><a-input v-model="form.network.private_subnets[record.zone]" class="cidr-input" /></template></a-table-column><a-table-column title="用途"><template #cell="{ record }"><a-space wrap><a-tag v-if="form.network.workload_subnet_zones?.includes(record.zone)" :color="form.network.workload_subnet_type === 'private' ? 'purple' : 'arcoblue'">{{ form.network.workload_subnet_type === 'private' ? 'Private' : 'Public' }} · EKS与组件</a-tag><a-tag v-if="form.network.data_subnet_zones?.includes(record.zone)" :color="form.network.data_subnet_type === 'private' ? 'purple' : 'green'">{{ form.network.data_subnet_type === 'private' ? 'Private' : 'Public' }} · 云服务</a-tag></a-space></template></a-table-column></template></a-table>
          </template>
          <template v-else>
            <a-alert type="warning" show-icon class="full-card">平台只在所选 VPC 中创建 EKS 和本环境资源，不会修改或销毁已有 VPC、子网、路由、NAT 与 Internet Gateway。EKS 创建后不能切换到另一个 VPC；所选子网需具备正确的出网路由，并至少分布在 2 个可用区。</a-alert>
            <a-alert v-if="vpcsError" type="error" show-icon class="full-card">{{ vpcsError }}</a-alert>
            <a-form :model="form.network" layout="vertical"><a-grid :cols="2" :col-gap="16">
              <a-grid-item :span="2"><a-form-item label="AWS VPC" required extra="只显示当前项目绑定凭据在当前 Region 可访问的 VPC。"><a-select v-model="form.network.existing_vpc_id" :loading="loadingVPCs" :disabled="baseReady" allow-search @popup-visible-change="(visible) => visible && loadVPCs()" @change="selectExistingVPC"><a-option v-for="vpc in vpcs" :key="vpc.id" :value="vpc.id">{{ vpc.name || '未命名 VPC' }} · {{ vpc.id }} · {{ vpc.cidr }}{{ vpc.default ? ' · 默认' : '' }}</a-option></a-select></a-form-item></a-grid-item>
              <a-grid-item><a-form-item label="EKS 与自建组件子网" required extra="选择 2–3 个不同可用区的子网，节点组将使用这些子网。"><a-select v-model="form.network.existing_workload_subnet_ids" multiple :max-tag-count="2" :disabled="baseReady" @change="syncExistingWorkloadZones"><a-option v-for="subnet in selectedVPCSubnets" :key="subnet.id" :value="subnet.id">{{ subnetLabel(subnet) }}</a-option></a-select></a-form-item></a-grid-item>
              <a-grid-item><a-form-item label="云中间件与云数据库子网" required extra="选择 2–3 个不同可用区的子网；托管服务只使用私网地址。"><a-select v-model="form.network.existing_data_subnet_ids" multiple :max-tag-count="2" @change="syncExistingDataZones"><a-option v-for="subnet in selectedVPCSubnets" :key="subnet.id" :value="subnet.id">{{ subnetLabel(subnet) }}</a-option></a-select></a-form-item></a-grid-item>
            </a-grid></a-form>
          </template>
        </a-card>
      </a-tab-pane>

	      <a-tab-pane v-if="!existingEKSTarget" key="eks">
          <template #title><span class="deployment-stage-tab-title"><span class="deployment-stage-badge phase-one">1</span><span>EKS 节点</span></span></template>
        <a-card class="form-card"><template #title><span class="card-title">Control Plane</span></template><a-form :model="form.eks" layout="vertical"><a-grid :cols="4" :col-gap="16"><a-grid-item><a-form-item label="Kubernetes 版本"><div class="eks-version-field"><a-select v-model="form.eks.kubernetes_version" :loading="loadingEKSVersions" allow-search><a-option v-if="!eksVersions.some((item) => item.version === form.eks.kubernetes_version)" :value="form.eks.kubernetes_version">{{ form.eks.kubernetes_version }} · 当前配置</a-option><a-option v-for="version in eksVersions" :key="version.version" :value="version.version">{{ version.version }} · {{ eksVersionStatus(version) }}{{ version.default ? ' · AWS默认' : '' }}</a-option></a-select><a-button :loading="loadingEKSVersions" @click="loadEKSVersions"><icon-refresh /></a-button></div><small class="field-help" :class="{ 'danger-text': eksVersionsError }">{{ eksVersionsError || '从当前 Region 的 AWS EKS API 实时读取' }}</small></a-form-item></a-grid-item><a-grid-item><a-form-item label="私网 API"><a-switch v-model="form.eks.endpoint_private_access" /></a-form-item></a-grid-item><a-grid-item><a-form-item label="公网 API"><a-switch v-model="form.eks.endpoint_public_access" /></a-form-item></a-grid-item><a-grid-item><a-form-item label="日志保留天数"><a-input-number v-model="form.eks.log_retention_days" :min="1" /></a-form-item></a-grid-item><a-grid-item :span="4"><a-form-item label="API 公网白名单（仅追加）"><a-input-tag v-model="form.eks.public_access_cidrs" /><small class="field-help">平台部署前会读取 AWS 当前白名单，只追加这里的 CIDR，不会删除或覆盖 AWS 控制台已有地址。</small></a-form-item></a-grid-item></a-grid></a-form></a-card>
        <a-alert v-if="form.eks.endpoint_public_access && form.eks.public_access_cidrs?.includes('0.0.0.0/0')" type="warning" show-icon class="full-card">EKS API 当前向全网开放，正式部署前请填写办公网/VPN出口CIDR。</a-alert>
        <a-alert v-if="nodePlanningLocked" type="info" show-icon class="full-card">EKS 已创建：已有节点组禁止删除或修改用途、规格、网络和磁盘；仍可新增节点组，也可调整已有节点组的 Min / Max 容量。新增节点组保存后执行阶段 1 更新即可创建。</a-alert>
        <a-alert v-else type="success" show-icon class="full-card">首次部署前规划节点用途：业务服务自动进入业务节点组，阶段 2 运维组件自动进入运维节点组。只有业务节点组使用 NoSchedule 污点；Ingress 和运维节点组仅通过标签与调度选择器绑定。EKS 创建成功后该规划不可修改。</a-alert>
        <a-card><template #title><span class="card-title">Managed Node Groups</span></template><template #extra><a-button type="primary" size="small" @click="nodeVisible = true"><icon-plus />添加节点组</a-button></template>
          <div class="node-grid">
            <div v-for="([name, group]) in nodeGroups" :key="name" class="node-card">
              <div class="node-card-head">
                <div><a-space><strong>{{ name }}</strong><a-tag v-if="nodePlanningLocked && nodeGroupActualDesired(name, group) !== null" size="small" color="arcoblue">AWS 当前 {{ nodeGroupActualDesired(name, group) }} 台</a-tag><a-tag v-if="nodePlanningLocked && !isPersistedNodeGroup(name)" size="small" color="green">待新增</a-tag><a-tag v-if="group.capacity_deferred" size="small" color="orange">容量暂缓</a-tag></a-space><small>{{ nodeRoleName(nodeRole(group)) }} · {{ (group.instance_types || []).length }} 种候选规格</small></div>
                <a-space><a-button size="mini" @click="openInstanceCatalog(name, group)">查询 AWS 规格</a-button><a-popconfirm :content="isPersistedNodeGroup(name) && nodePlanningLocked ? '已有节点组受保护，不能删除' : '删除该节点组？'" :disabled="isPersistedNodeGroup(name) && nodePlanningLocked" @ok="removeNodeGroup(name)"><a-button size="mini" status="danger" :disabled="isPersistedNodeGroup(name) && nodePlanningLocked"><icon-delete /></a-button></a-popconfirm></a-space>
              </div>
              <a-form :model="group" layout="vertical"><a-grid :cols="3" :col-gap="10">
                <a-grid-item><a-form-item label="节点组用途" required><a-select :model-value="nodeRole(group)" :disabled="nodeGroupFieldLocked(name)" @change="updateNodeRole(name, group, $event)"><a-option v-for="role in nodeRoleOptions" :key="role.value" :value="role.value">{{ role.label }}</a-option></a-select></a-form-item></a-grid-item>
                <a-grid-item :span="2"><a-form-item label="调度绑定"><a-input :model-value="nodeSchedulingHint(name, group)" disabled /></a-form-item></a-grid-item>
                <a-grid-item :span="2"><a-form-item label="实例类型" required extra="至少保留 1 种、最多 20 种；平台会按顺序作为 EKS 节点候选规格。"><a-input-tag :model-value="group.instance_types || []" :disabled="nodeGroupFieldLocked(name)" @change="updateNodeGroupInstanceTypes(name, group, $event)" /></a-form-item></a-grid-item>
                <a-grid-item><a-form-item label="容量"><a-select v-model="group.capacity_type" :disabled="nodeGroupFieldLocked(name)"><a-option value="ON_DEMAND">ON_DEMAND</a-option><a-option value="SPOT">SPOT</a-option></a-select></a-form-item></a-grid-item>
                <a-grid-item><a-form-item label="节点网络"><a-select v-model="group.subnet_type" :disabled="nodeGroupFieldLocked(name)"><a-option value="private">Private · NAT 固定出口</a-option><a-option value="public">Public · 节点公网出口</a-option></a-select></a-form-item></a-grid-item>
                <a-grid-item :span="2"><a-form-item label="AZ"><a-select v-model="group.availability_zones" multiple :disabled="nodeGroupFieldLocked(name)"><a-option v-for="zone in form.network.availability_zones" :key="zone" :value="zone">{{ zone }}</a-option></a-select></a-form-item></a-grid-item>
                <a-grid-item><a-form-item label="Min"><a-input-number v-model="group.min_size" :min="0" /></a-form-item></a-grid-item>
                <a-grid-item><a-form-item :label="nodeGroupFieldLocked(name) ? 'AWS 当前节点数' : '初始节点数'" :extra="nodeGroupFieldLocked(name) ? `由 Cluster Autoscaler 管理；配置初始值 ${group.desired_size ?? 0} 不会强制覆盖 AWS 当前容量。` : '仅用于首次创建该节点组。'"><a-input-number v-if="nodeGroupFieldLocked(name)" :model-value="nodeGroupActualDesired(name, group) ?? group.desired_size" :min="0" disabled /><a-input-number v-else v-model="group.desired_size" :min="0" /></a-form-item></a-grid-item>
                <a-grid-item><a-form-item label="Max"><a-input-number v-model="group.max_size" :min="1" /></a-form-item></a-grid-item>
                <a-grid-item><a-form-item label="磁盘 GiB"><a-input-number v-model="group.disk_size" :min="20" :disabled="nodeGroupFieldLocked(name)" /></a-form-item></a-grid-item>
                <a-grid-item :span="2"><a-form-item label="容量执行" extra="暂缓时保留规划的 Min / 初始节点数 / Max，但 Terraform 实际以 0/0/Max 创建节点组；配额通过后切换为启用并执行阶段 1。"><a-switch v-model="group.capacity_deferred" checked-text="暂缓" unchecked-text="启用" /></a-form-item></a-grid-item>
	                <a-grid-item v-if="group.capacity_deferred" :span="3"><a-alert type="warning" show-icon>当前只建立节点组、标签、业务组污点和 State，不主动启动 EC2；Cluster Autoscaler 仍可在出现匹配的 Pending Pod 时按 Max 自动扩容。</a-alert></a-grid-item>
              </a-grid></a-form>
            </div>
          </div>
        </a-card>
      </a-tab-pane>

	      <a-tab-pane v-if="!existingEKSTarget" key="data">
          <template #title><span class="deployment-stage-tab-title"><span class="deployment-stage-badge phase-one">1</span><span>云中间件与云数据库</span></span></template>
        <a-alert type="info" show-icon class="full-card">以下均为 AWS 可直接购买的托管云服务。启用后可调整规格、单例/集群或 Serverless 模式；部署成功后连接地址和凭据会进入“资源与访问”。</a-alert>
        <a-card class="full-card"><template #title><span class="card-title">云服务部署网络</span></template><template #extra><a-button size="small" @click="activeTab = 'network'">完整网络规划</a-button></template>
          <template v-if="form.network.mode === 'create'">
            <a-alert type="info" show-icon class="full-card">阶段 1 将新建 VPC {{ form.network.vpc_cidr }}，所有已启用云服务自动创建在下面选择的子网中。选择 Public 只表示使用 Public 类型子网；RDS、Redis、Kafka 等服务仍关闭公网访问，只提供 VPC 私网地址。</a-alert>
            <a-form :model="form.network" layout="vertical"><a-grid :cols="2" :col-gap="16">
              <a-grid-item><a-form-item label="云服务子网类型"><a-radio-group v-model="form.network.data_subnet_type" type="button"><a-radio value="public">Public 子网</a-radio><a-radio value="private">Private 子网</a-radio></a-radio-group></a-form-item></a-grid-item>
              <a-grid-item><a-form-item label="云服务可用区" extra="数据库子网组至少需要两个不同可用区。"><a-select v-model="form.network.data_subnet_zones" multiple><a-option v-for="zone in form.network.availability_zones" :key="zone" :value="zone">{{ zone }} · {{ form.network.data_subnet_type === 'private' ? form.network.private_subnets[zone] : form.network.public_subnets[zone] }}</a-option></a-select></a-form-item></a-grid-item>
            </a-grid></a-form>
          </template>
          <template v-else>
            <a-alert type="warning" show-icon class="full-card">云服务将创建到当前项目凭据所选的已有 VPC；平台不会修改该 VPC 的路由、NAT 或网关。EKS 工作负载子网仍在“网络规划”中单独选择。</a-alert>
            <a-form :model="form.network" layout="vertical"><a-grid :cols="2" :col-gap="16">
              <a-grid-item><a-form-item label="已有 VPC" required><a-select v-model="form.network.existing_vpc_id" :loading="loadingVPCs" :disabled="baseReady" allow-search @popup-visible-change="(visible) => visible && loadVPCs()" @change="selectExistingVPC"><a-option v-for="vpc in vpcs" :key="vpc.id" :value="vpc.id">{{ vpc.name || '未命名 VPC' }} · {{ vpc.id }} · {{ vpc.cidr }}</a-option></a-select></a-form-item></a-grid-item>
              <a-grid-item><a-form-item label="云服务子网" required extra="选择 2–3 个不同可用区的子网。"><a-select v-model="form.network.existing_data_subnet_ids" multiple :max-tag-count="2" @change="syncExistingDataZones"><a-option v-for="subnet in selectedVPCSubnets" :key="subnet.id" :value="subnet.id">{{ subnetLabel(subnet) }}</a-option></a-select></a-form-item></a-grid-item>
            </a-grid></a-form>
          </template>
        </a-card>
	        <div class="cloud-service-workspace">
	          <aside class="cloud-service-palette">
	            <div class="cloud-service-pane-heading"><div><strong>未启用云服务</strong><span>点击启用后会移到右侧，不会重复创建</span></div><a-tag>{{ availableCloudServices.length }}</a-tag></div>
	            <div v-if="availableCloudServices.length" class="cloud-service-palette-list">
	              <button v-for="service in availableCloudServices" :key="service.key" type="button" class="cloud-service-palette-item" @click="enableCloudService(service)"><span class="cloud-service-symbol">{{ service.short }}</span><span><strong>{{ service.title }}</strong><small>{{ service.description }}</small></span><icon-plus /></button>
	            </div>
	            <a-empty v-else description="所有云服务均已启用" />
	          </aside>
	          <section class="cloud-service-editor-pane">
	            <div class="cloud-service-pane-heading"><div><strong>已启用云服务</strong><span>修改的是目标配置，更新部署会收敛到目标值</span></div><a-tag color="green">已启用 {{ enabledCloudServices.length }}</a-tag></div>
	            <div v-if="enabledCloudServices.length" class="cloud-service-tabs">
	              <button v-for="service in enabledCloudServices" :key="service.key" type="button" :class="{ active: activeCloudService === service.key }" @click="activeCloudService = service.key"><i />{{ service.title }}</button>
	            </div>
	            <div v-if="enabledCloudServices.length" class="data-grid">
	          <data-service-card title="RDS MySQL" :model="form.data_services.rds"><template #form>
	            <a-grid :cols="2" :col-gap="10"><a-grid-item><a-form-item label="凭证管理" extra="新建默认自管；密码加密保存，不写入环境 YAML。"><a-select v-model="form.data_services.rds.credential_management"><a-option value="self-managed">自我管理凭证</a-option><a-option value="aws-managed">AWS Secrets Manager 托管</a-option></a-select></a-form-item></a-grid-item><a-grid-item><a-form-item label="管理员用户名"><a-input v-model="form.data_services.rds.master_username" /></a-form-item></a-grid-item><a-grid-item v-if="form.data_services.rds.credential_management === 'self-managed'" :span="2"><a-form-item label="管理员密码" :extra="dataServiceCredentialHint('rds')"><a-input-password v-model="dataServicePasswords.rds" :placeholder="dataServiceCredentialConfigured('rds') ? '留空保留已加密保存的密码' : '请输入 8-41 位密码'" allow-clear /></a-form-item></a-grid-item></a-grid>
	            <a-form-item label="实例规格"><div class="eks-version-field"><a-select v-model="form.data_services.rds.instance_class" :loading="cloudCatalogLoading('rds-mysql')" allow-search @popup-visible-change="(visible) => visible && loadCloudServiceOptions('rds-mysql', form.data_services.rds.engine_version)"><a-option v-if="cloudCurrentMissing('rds-mysql', form.data_services.rds.instance_class)" :value="form.data_services.rds.instance_class">{{ form.data_services.rds.instance_class }} · 当前配置（AWS未确认）</a-option><a-option v-for="option in cloudServiceOptions('rds-mysql')" :key="option.value" :value="option.value">{{ option.value }}{{ option.multi_az_capable ? ' · 支持Multi-AZ' : '' }}</a-option></a-select><a-button :loading="cloudCatalogLoading('rds-mysql')" @click="loadCloudServiceOptions('rds-mysql', form.data_services.rds.engine_version, true)"><icon-refresh /></a-button></div><small class="field-help" :class="{ 'danger-text': cloudCatalogError('rds-mysql') }">{{ cloudCatalogHint('rds-mysql', 'RDS API') }}</small></a-form-item><a-grid :cols="2" :col-gap="10"><a-grid-item><a-form-item label="数据库名"><a-input v-model="form.data_services.rds.database_name" /></a-form-item></a-grid-item><a-grid-item><a-form-item label="存储 GiB" extra="精确目标容量；AWS RDS 只支持扩容，不支持缩容。"><a-input-number v-model="form.data_services.rds.allocated_storage" :min="20" /></a-form-item></a-grid-item><a-grid-item><a-form-item label="Multi-AZ"><a-switch v-model="form.data_services.rds.multi_az" /></a-form-item></a-grid-item><a-grid-item><a-form-item label="删除保护"><a-switch v-model="form.data_services.rds.deletion_protection" /></a-form-item></a-grid-item></a-grid>
	          </template></data-service-card>
	          <data-service-card title="Aurora MySQL" :model="form.data_services.aurora"><template #form>
	            <a-grid :cols="2" :col-gap="10"><a-grid-item><a-form-item label="凭证管理" extra="新建默认自管；部署后凭证可在资源与访问中查看。"><a-select v-model="form.data_services.aurora.credential_management"><a-option value="self-managed">自我管理凭证</a-option><a-option value="aws-managed">AWS Secrets Manager 托管</a-option></a-select></a-form-item></a-grid-item><a-grid-item><a-form-item label="管理员用户名"><a-input v-model="form.data_services.aurora.master_username" /></a-form-item></a-grid-item><a-grid-item v-if="form.data_services.aurora.credential_management === 'self-managed'" :span="2"><a-form-item label="管理员密码" :extra="dataServiceCredentialHint('aurora')"><a-input-password v-model="dataServicePasswords.aurora" :placeholder="dataServiceCredentialConfigured('aurora') ? '留空保留已加密保存的密码' : '请输入 8-41 位密码'" allow-clear /></a-form-item></a-grid-item></a-grid>
	            <a-form-item label="模式"><a-select v-model="form.data_services.aurora.mode"><a-option value="serverless-v2">Serverless v2</a-option></a-select></a-form-item><a-form-item label="数据库名"><a-input v-model="form.data_services.aurora.database_name" /></a-form-item><a-grid :cols="3" :col-gap="10"><a-grid-item><a-form-item label="实例数"><a-input-number v-model="form.data_services.aurora.instance_count" :min="1" :max="15" /></a-form-item></a-grid-item><a-grid-item><a-form-item label="最小ACU"><a-input-number v-model="form.data_services.aurora.min_acu" :min="0" :step="0.5" /></a-form-item></a-grid-item><a-grid-item><a-form-item label="最大ACU"><a-input-number v-model="form.data_services.aurora.max_acu" :min="0.5" :step="0.5" /></a-form-item></a-grid-item></a-grid><a-grid :cols="2" :col-gap="10"><a-grid-item><a-form-item label="启用回溯" extra="用于快速将 Aurora MySQL 集群回溯到特定时间点；AWS 会对回溯变更记录收取存储费用。"><a-switch v-model="form.data_services.aurora.backtrack_enabled" /></a-form-item></a-grid-item><a-grid-item v-if="form.data_services.aurora.backtrack_enabled"><a-form-item label="回溯窗口（小时）" extra="AWS 最大支持 72 小时；新建默认 72 小时。"><a-input-number v-model="form.data_services.aurora.backtrack_window_hours" :min="1" :max="72" /></a-form-item></a-grid-item></a-grid><a-alert v-if="form.data_services.aurora.backtrack_enabled" type="warning" show-icon class="full-card">回溯只支持 Aurora MySQL。已有集群如果创建时未启用回溯，AWS 可能拒绝直接补开；平台会在部署前读取实际状态并提前阻止不支持的操作。</a-alert>
	          </template></data-service-card>
	          <data-service-card title="RDS PostgreSQL" :model="form.data_services.postgres"><template #form><a-form-item label="实例规格"><div class="eks-version-field"><a-select v-model="form.data_services.postgres.instance_class" :loading="cloudCatalogLoading('rds-postgres')" allow-search @popup-visible-change="(visible) => visible && loadCloudServiceOptions('rds-postgres', form.data_services.postgres.engine_version)"><a-option v-if="cloudCurrentMissing('rds-postgres', form.data_services.postgres.instance_class)" :value="form.data_services.postgres.instance_class">{{ form.data_services.postgres.instance_class }} · 当前配置（AWS未确认）</a-option><a-option v-for="option in cloudServiceOptions('rds-postgres')" :key="option.value" :value="option.value">{{ option.value }}{{ option.multi_az_capable ? ' · 支持Multi-AZ' : '' }}</a-option></a-select><a-button :loading="cloudCatalogLoading('rds-postgres')" @click="loadCloudServiceOptions('rds-postgres', form.data_services.postgres.engine_version, true)"><icon-refresh /></a-button></div><small class="field-help" :class="{ 'danger-text': cloudCatalogError('rds-postgres') }">{{ cloudCatalogHint('rds-postgres', 'RDS API') }}</small></a-form-item><a-grid :cols="2" :col-gap="10"><a-grid-item><a-form-item label="数据库名"><a-input v-model="form.data_services.postgres.database_name" /></a-form-item></a-grid-item><a-grid-item><a-form-item label="存储 GiB" extra="精确目标容量；AWS RDS 只支持扩容，不支持缩容。"><a-input-number v-model="form.data_services.postgres.allocated_storage" :min="20" /></a-form-item></a-grid-item><a-grid-item><a-form-item label="Multi-AZ"><a-switch v-model="form.data_services.postgres.multi_az" /></a-form-item></a-grid-item></a-grid></template></data-service-card>
	          <data-service-card title="Amazon DocumentDB（MongoDB 兼容）" :model="form.data_services.documentdb"><template #form><a-alert type="info" show-icon class="full-card">AWS 托管的 MongoDB 兼容数据库；主密码由 Secrets Manager 生成和轮换，连接强制使用 TLS。</a-alert><a-grid :cols="2" :col-gap="10"><a-grid-item><a-form-item label="实例规格"><div class="eks-version-field"><a-select v-model="form.data_services.documentdb.instance_class" :loading="cloudCatalogLoading('documentdb')" allow-search @popup-visible-change="(visible) => visible && loadCloudServiceOptions('documentdb', form.data_services.documentdb.engine_version)"><a-option v-if="cloudCurrentMissing('documentdb', form.data_services.documentdb.instance_class)" :value="form.data_services.documentdb.instance_class">{{ form.data_services.documentdb.instance_class }} · 当前配置（AWS未确认）</a-option><a-option v-for="option in cloudServiceOptions('documentdb')" :key="option.value" :value="option.value">{{ option.value }}</a-option></a-select><a-button :loading="cloudCatalogLoading('documentdb')" @click="loadCloudServiceOptions('documentdb', form.data_services.documentdb.engine_version, true)"><icon-refresh /></a-button></div><small class="field-help" :class="{ 'danger-text': cloudCatalogError('documentdb') }">{{ cloudCatalogHint('documentdb', 'DocumentDB API') }}</small></a-form-item></a-grid-item><a-grid-item><a-form-item label="实例数量"><a-input-number v-model="form.data_services.documentdb.instance_count" :min="1" :max="16" /></a-form-item></a-grid-item><a-grid-item><a-form-item label="管理员用户名"><a-input v-model="form.data_services.documentdb.master_username" /></a-form-item></a-grid-item><a-grid-item><a-form-item label="存储类型"><a-select v-model="form.data_services.documentdb.storage_type"><a-option value="standard">Standard</a-option><a-option value="iopt1">I/O-Optimized</a-option></a-select></a-form-item></a-grid-item><a-grid-item><a-form-item label="删除保护"><a-switch v-model="form.data_services.documentdb.deletion_protection" /></a-form-item></a-grid-item></a-grid></template></data-service-card>
          <data-service-card title="AWS ElastiCache（Redis / Valkey）" :model="form.data_services.elasticache"><template #form><a-alert type="info" show-icon class="full-card">Redis / Valkey 云缓存由 AWS ElastiCache 托管。容量使用“精确目标值”，更新部署会扩容或缩容到这里填写的数量，不会在现有节点上累加。</a-alert><a-form-item label="模式"><a-select v-model="form.data_services.elasticache.mode"><a-option value="cluster">节点集群</a-option><a-option value="serverless">Serverless</a-option></a-select></a-form-item><a-form-item v-if="form.data_services.elasticache.mode !== 'serverless'" label="节点规格"><div class="eks-version-field"><a-select v-model="form.data_services.elasticache.node_type" :loading="cloudCatalogLoading('elasticache')" allow-search @popup-visible-change="(visible) => visible && loadCloudServiceOptions('elasticache')"><a-option v-if="cloudCurrentMissing('elasticache', form.data_services.elasticache.node_type)" :value="form.data_services.elasticache.node_type">{{ form.data_services.elasticache.node_type }} · 当前配置（AWS未确认）</a-option><a-option v-for="option in cloudServiceOptions('elasticache')" :key="option.value" :value="option.value">{{ option.value }}</a-option></a-select><a-button :loading="cloudCatalogLoading('elasticache')" @click="loadCloudServiceOptions('elasticache', '', true)"><icon-refresh /></a-button></div><small class="field-help" :class="{ 'danger-text': cloudCatalogError('elasticache') }">{{ cloudCatalogHint('elasticache', 'AWS Pricing API') }}</small></a-form-item><template v-if="form.data_services.elasticache.mode !== 'serverless'"><a-grid :cols="2" :col-gap="10"><a-grid-item><a-form-item label="分片数量（Shard）" extra="每个分片固定包含 1 个主节点。"><a-input-number v-model="form.data_services.elasticache.num_node_groups" :min="1" :max="500" /></a-form-item></a-grid-item><a-grid-item><a-form-item label="每分片总节点数" extra="包含主节点；1 表示无只读副本，2 表示主节点 + 1 个副本。"><a-input-number v-model="form.data_services.elasticache.nodes_per_shard" :min="1" :max="6" /></a-form-item></a-grid-item></a-grid><a-alert :type="elasticacheTotalNodes > 90 ? 'warning' : 'success'" show-icon class="full-card">当前目标：{{ Number(form.data_services.elasticache.num_node_groups || 0) }} 个分片 × {{ Number(form.data_services.elasticache.nodes_per_shard || 0) }} 个节点 = {{ elasticacheTotalNodes }} 个总节点；每分片只读副本 {{ Math.max(0, Number(form.data_services.elasticache.nodes_per_shard || 1) - 1) }} 个。</a-alert></template><a-grid v-else :cols="2" :col-gap="10"><a-grid-item><a-form-item label="最大存储 GB"><a-input-number v-model="form.data_services.elasticache.max_storage_gb" :min="1" /></a-form-item></a-grid-item><a-grid-item><a-form-item label="最大 ECPU/s"><a-input-number v-model="form.data_services.elasticache.max_ecpu" :min="1000" /></a-form-item></a-grid-item></a-grid></template></data-service-card>
          <data-service-card title="Amazon MSK Kafka" :model="form.data_services.msk"><template #form><a-form-item label="模式"><a-select v-model="form.data_services.msk.mode"><a-option value="serverless">Serverless</a-option><a-option value="provisioned">预置集群</a-option></a-select></a-form-item><template v-if="form.data_services.msk.mode === 'provisioned'"><a-form-item label="Broker规格"><div class="eks-version-field"><a-select v-model="form.data_services.msk.instance_type" :loading="cloudCatalogLoading('msk')" allow-search @popup-visible-change="(visible) => visible && loadCloudServiceOptions('msk')"><a-option v-if="cloudCurrentMissing('msk', form.data_services.msk.instance_type)" :value="form.data_services.msk.instance_type">{{ form.data_services.msk.instance_type }} · 当前配置（AWS未确认）</a-option><a-option v-for="option in cloudServiceOptions('msk')" :key="option.value" :value="option.value">{{ option.value }}</a-option></a-select><a-button :loading="cloudCatalogLoading('msk')" @click="loadCloudServiceOptions('msk', '', true)"><icon-refresh /></a-button></div><small class="field-help" :class="{ 'danger-text': cloudCatalogError('msk') }">{{ cloudCatalogHint('msk', 'AWS Pricing API') }}</small></a-form-item><a-grid :cols="2" :col-gap="10"><a-grid-item><a-form-item label="Broker 数量" :extra="`必须是当前 ${dataSubnetZoneCount} 个数据可用区的整数倍；AWS 只支持增加。`"><a-input-number v-model="form.data_services.msk.broker_count" :min="dataSubnetZoneCount" :step="dataSubnetZoneCount" /></a-form-item></a-grid-item><a-grid-item><a-form-item label="单 Broker 磁盘 GiB" extra="AWS MSK 磁盘只支持扩容，不支持缩容。"><a-input-number v-model="form.data_services.msk.volume_size" :min="1" /></a-form-item></a-grid-item></a-grid></template><a-form-item label="认证"><a-input model-value="SASL/IAM + TLS" disabled /></a-form-item></template></data-service-card>
          <data-service-card title="Amazon MQ RabbitMQ" :model="form.data_services.amazon_mq"><template #form><a-form-item label="部署模式"><a-select v-model="form.data_services.amazon_mq.deployment_mode"><a-option value="SINGLE_INSTANCE">单实例</a-option><a-option value="CLUSTER_MULTI_AZ">三节点多AZ集群</a-option></a-select></a-form-item><a-form-item label="实例规格"><div class="eks-version-field"><a-select v-model="form.data_services.amazon_mq.host_instance_type" :loading="cloudCatalogLoading('amazon-mq')" allow-search @popup-visible-change="(visible) => visible && loadCloudServiceOptions('amazon-mq', form.data_services.amazon_mq.engine_version)"><a-option v-if="cloudCurrentMissing('amazon-mq', form.data_services.amazon_mq.host_instance_type)" :value="form.data_services.amazon_mq.host_instance_type">{{ form.data_services.amazon_mq.host_instance_type }} · 当前配置（AWS未确认）</a-option><a-option v-for="option in amazonMQOptions" :key="option.value" :value="option.value">{{ option.value }} · {{ (option.deployment_modes || []).join(' / ') }}</a-option></a-select><a-button :loading="cloudCatalogLoading('amazon-mq')" @click="loadCloudServiceOptions('amazon-mq', form.data_services.amazon_mq.engine_version, true)"><icon-refresh /></a-button></div><small class="field-help" :class="{ 'danger-text': cloudCatalogError('amazon-mq') }">{{ cloudCatalogHint('amazon-mq', 'Amazon MQ API') }}</small></a-form-item><a-form-item label="管理员用户名"><a-input v-model="form.data_services.amazon_mq.master_username" /></a-form-item></template></data-service-card>
          <data-service-card title="Amazon ECR 镜像仓库" :model="form.ecr"><template #form><a-form-item label="Repositories"><a-input-tag v-model="form.ecr.repositories" /></a-form-item><a-form-item label="保留镜像数"><a-input-number v-model="form.ecr.keep_image_count" /></a-form-item></template></data-service-card>
	            </div>
	            <div v-else class="cloud-service-editor-empty"><a-empty description="请从左侧选择需要创建的云服务" /></div>
	          </section>
		        </div>
		        <a-card class="full-card"><template #title><span class="card-title">阶段 1 · EKS 基础服务</span></template><a-alert type="info" show-icon class="full-card">Consul 与 etcd 是阶段 1 的有状态基础服务。开启或关闭都会先显示安装/卸载影响；确认、保存后，由“开始/更新部署【阶段一】”完成对账。</a-alert><div class="component-options"><div v-for="component in baseServiceComponents" :key="component.key" class="component-option" :class="{ active: componentEnabled(component.config_path) }"><a-checkbox :model-value="componentEnabled(component.config_path)" @change="requestComponentToggle(component, Boolean($event))" /><div class="component-option-content"><em>基础服务</em><strong>{{ component.display_name }}</strong><small>{{ component.description }}</small><a-tag v-if="component.key === 'etcd' && componentEnabled(component.config_path)" color="green">WebUI {{ componentWebUIConfig(component).enabled ? '已开启' : '已关闭' }}</a-tag><a-tag :color="componentLifecycleColor(component)">{{ componentLifecycleLabel(component) }}</a-tag></div></div></div></a-card>
		        <a-card v-if="statefulBaseComponents.length" class="full-card">
		          <template #title><span class="card-title">Consul / etcd 基础服务参数</span></template>
		          <a-alert type="warning" show-icon class="full-card">下列配置只在“开始/更新部署【阶段一】”时生效。服务关闭后仍保留参数，便于确认 PVC 与备份保留策略或重新启用。</a-alert>
		          <a-collapse>
		            <a-collapse-item
		              v-for="component in statefulBaseComponents"
		              :key="component.key"
		              :header="`${component.display_name} · ${componentLifecycleLabel(component)} · ${componentRuntimeConfig(component).storage_class}/${componentRuntimeConfig(component).storage_size}`"
		            >
		              <a-form :model="componentRuntimeConfig(component)" layout="vertical">
		                <a-grid :cols="4" :col-gap="14">
		                  <a-grid-item><a-form-item label="部署模式"><a-radio-group :model-value="componentMode(component)" type="button" @change="setComponentMode(component, String($event))"><a-radio value="standalone">单机</a-radio><a-radio value="cluster">集群</a-radio></a-radio-group></a-form-item></a-grid-item>
		                  <a-grid-item><a-form-item label="副本数"><a-input-number v-model="componentRuntimeConfig(component).replicas" :min="componentMode(component) === 'cluster' ? 3 : 1" :max="9" :disabled="componentMode(component) !== 'cluster'" /></a-form-item></a-grid-item>
		                  <a-grid-item><a-form-item label="Namespace"><a-select v-model="componentRuntimeConfig(component).namespace"><a-option v-for="row in componentNamespaceRows" :key="row.name" :value="row.name">{{ row.name }}</a-option></a-select></a-form-item></a-grid-item>
		                  <a-grid-item><a-form-item label="引擎版本"><a-select v-model="componentRuntimeConfig(component).image" allow-search><a-option v-for="image in baseComponentImageOptions(component)" :key="image" :value="image">{{ imageVersionLabel(image) }}</a-option></a-select></a-form-item></a-grid-item>
		                  <a-grid-item><a-form-item label="卸载服务时保留 PVC"><a-switch v-model="componentRuntimeConfig(component).retain_pvc_on_delete" /></a-form-item></a-grid-item>
		                  <a-grid-item><a-form-item label="单副本磁盘容量"><a-input v-model="componentRuntimeConfig(component).storage_size" placeholder="20Gi" /></a-form-item></a-grid-item>
		                  <a-grid-item><a-form-item label="StorageClass"><a-input v-model="componentRuntimeConfig(component).storage_class" /></a-form-item></a-grid-item>
		                  <a-grid-item v-if="component.key === 'etcd'"><a-form-item label="部署 Web 管理页"><a-switch v-model="componentWebUIConfig(component).enabled" /></a-form-item></a-grid-item>
		                  <a-grid-item><a-form-item label="保留 S3 备份策略"><a-switch v-model="componentRuntimeConfig(component).backup.enabled" /></a-form-item></a-grid-item>
		                  <a-grid-item v-if="componentRuntimeConfig(component).backup.enabled"><a-form-item label="备份 Cron"><a-input v-model="componentRuntimeConfig(component).backup.schedule" /></a-form-item></a-grid-item>
		                  <a-grid-item v-if="componentRuntimeConfig(component).backup.enabled"><a-form-item label="备份保留天数"><a-input-number v-model="componentRuntimeConfig(component).backup.retention_days" :min="1" :max="3650" /></a-form-item></a-grid-item>
		                </a-grid>
		                <a-alert :type="componentEnabled(component.config_path) || componentActual(component) ? 'info' : 'normal'" show-icon>
		                  {{ baseComponentLifecycleHint(component) }}
		                </a-alert>
		              </a-form>
		            </a-collapse-item>
		          </a-collapse>
		        </a-card>
	      </a-tab-pane>

	      <a-tab-pane key="components">
          <template #title><span class="deployment-stage-tab-title"><span class="deployment-stage-badge phase-two">2</span><span>安装组件</span></span></template>
	        <a-card class="full-card"><template #title><span class="card-title">可扩展自建组件目录</span></template><a-alert type="info" show-icon class="full-card">组件开关表示目标状态：开启后会安装或更新，关闭后会在阶段 2 卸载。卸载前会检查依赖、域名路由和数据保留策略。</a-alert><div class="component-options"><div v-for="component in stageTwoComponents" :key="component.key" class="component-option" :class="{ active: componentEnabled(component.config_path) }"><a-checkbox :model-value="componentEnabled(component.config_path)" @change="requestComponentToggle(component, Boolean($event))" /><div class="component-option-content"><em>{{ component.category }}</em><strong>{{ component.display_name }}</strong><small>{{ component.description }}</small><a-radio-group v-if="componentEnabled(component.config_path)" :model-value="componentMode(component)" type="button" size="mini" class="component-mode" @change="setComponentMode(component, String($event))"><a-radio value="standalone">单机</a-radio><a-radio value="cluster" :disabled="componentStandaloneOnly(component)">集群</a-radio></a-radio-group><a-tag :color="componentLifecycleColor(component)">{{ componentLifecycleLabel(component) }}</a-tag></div></div></div></a-card>
	        <a-card v-if="clickVisualEnabled" class="clickvisual-storage-card full-card">
	          <template #title><span class="card-title">ClickVisual 磁盘与容量</span></template>
	          <template #extra><a-button v-if="clickVisualDeployed" :loading="loadingManagedStorage" :disabled="dirty || environmentBusy || !baseReady" @click="loadManagedStorage"><icon-refresh />刷新实际容量</a-button></template>
	          <a-alert :type="clickVisualDeployed ? 'info' : 'success'" show-icon class="full-card">
	            {{ clickVisualDeployed
	              ? '当前显示 EKS 中的实际 PVC 容量。扩容会创建独立任务并在线增大磁盘；Kafka 会同时扩容所有活动 Broker PVC。'
	              : '这里配置首次部署容量。ClickVisual 部署成功后，会自动切换为 EKS 实际容量和在线扩容入口。' }}
	          </a-alert>
	          <div class="clickvisual-storage-grid">
	            <div v-for="storage in clickVisualStorageComponents" :key="storage.key" class="clickvisual-storage-item">
	              <div class="clickvisual-storage-item__head"><div><strong>{{ storage.name }}</strong><small>{{ storage.description }}</small></div><a-tag :color="clickVisualDeployed && clickVisualActiveStorage(storage.key).length ? 'green' : 'arcoblue'">{{ clickVisualDeployed ? clickVisualStorageSummary(storage.key) : '首次部署' }}</a-tag></div>
	              <a-form :model="{}" layout="vertical">
	                <a-form-item :label="clickVisualDeployed ? '平台记录的目标容量（GiB）' : '首次部署容量（GiB）'" :extra="clickVisualDeployed ? '运行中请使用扩容按钮，不直接改动初始值。' : storage.initialHint">
	                  <a-input-number :model-value="clickVisualConfiguredSizeGi(storage.key)" :min="1" :max="16384" :disabled="clickVisualDeployed" style="width:100%" @change="setClickVisualConfiguredSizeGi(storage.key, Number($event))" />
	                </a-form-item>
	              </a-form>
	              <div v-if="clickVisualDeployed" class="clickvisual-storage-item__runtime">
	                <span>{{ clickVisualStorageDetail(storage.key) }}</span>
	                <a-button type="primary" size="small" :disabled="!clickVisualExpandableStorage(storage.key) || !store.canDeploy || environmentBusy" @click="openClickVisualStorageExpand(storage.key)">扩容</a-button>
	              </div>
	            </div>
	          </div>
	          <div class="clickvisual-storage-class"><div><strong>StorageClass</strong><small>首次部署可设置；部署后为了避免 PVC 迁移风险，不允许直接修改。</small></div><a-input :model-value="clickVisualStorageClass" :disabled="clickVisualDeployed" @change="setClickVisualStorageClass(String($event))" /></div>
	          <a-table v-if="clickVisualDeployed" :data="managedStorageItems" :loading="loadingManagedStorage" :pagination="false" size="small" row-key="pvc_name" class="clickvisual-storage-table">
	            <template #columns>
	              <a-table-column title="子组件" data-index="component"><template #cell="{ record }"><strong>{{ managedStorageName(record.component) }}</strong></template></a-table-column>
	              <a-table-column title="PVC"><template #cell="{ record }"><span class="managed-storage-pvc">{{ record.namespace }}/{{ record.pvc_name }}</span></template></a-table-column>
	              <a-table-column title="请求 / 实际"><template #cell="{ record }">{{ record.requested || '—' }} / {{ record.capacity || '—' }}</template></a-table-column>
	              <a-table-column title="存储类"><template #cell="{ record }">{{ record.storage_class }}<a-tag v-if="record.allow_expansion" color="green" size="small">可在线扩容</a-tag></template></a-table-column>
	              <a-table-column title="状态"><template #cell="{ record }"><a-tag :color="record.active && record.phase === 'Bound' ? 'green' : 'gray'">{{ record.active ? record.phase : '已保留旧盘' }}</a-tag></template></a-table-column>
	              <a-table-column title="操作" :width="180"><template #cell="{ record }"><a-space v-if="record.active"><a-button size="mini" type="primary" :disabled="!record.allow_expansion || !store.canDeploy || environmentBusy" @click="openManagedStorageResize(record, 'expand')">扩容</a-button><a-button size="mini" status="warning" :disabled="!store.canDeploy || environmentBusy" @click="openManagedStorageResize(record, 'shrink')">安全缩容</a-button></a-space><span v-else>保留用于回滚</span></template></a-table-column>
	            </template>
	            <template #empty><a-empty :description="managedStorageLoaded ? '未发现 ClickVisual 活动 PVC' : '正在读取 Kafka、ClickHouse 和 MySQL 实际 PVC 容量'" /></template>
	          </a-table>
	        </a-card>
	        <a-card><template #title><span class="card-title">组件部署参数</span></template><a-alert type="info" show-icon class="full-card">默认只显示部署必需项。需要覆盖超时、控制台访问、资源限制或 Helm Values 时，请在“高级参数”中按需选择。</a-alert>
	          <a-empty v-if="!catalogComponents.length" description="请先在上方勾选需要安装的组件" />
	          <a-collapse v-else><a-collapse-item v-for="component in catalogComponents" :key="component.key" :header="`${component.display_name} · ${catalogConfig(component.key).namespace} · ${componentValuesSummary(component.key)}`">
		            <a-form :model="catalogConfig(component.key)" layout="vertical"><a-grid :cols="4" :col-gap="14"><a-grid-item><a-form-item label="部署模式"><a-radio-group v-model="catalogConfig(component.key).deployment_mode" type="button" @change="setComponentMode(component, String($event))"><a-radio value="standalone">单机</a-radio><a-radio value="cluster" :disabled="componentStandaloneOnly(component)">集群</a-radio></a-radio-group></a-form-item></a-grid-item><a-grid-item><a-form-item label="运行副本" :extra="componentReplicaHint(component)"><a-input-number v-model="catalogConfig(component.key).replicas" :min="catalogConfig(component.key).deployment_mode === 'cluster' ? 2 : 1" :max="20" :disabled="catalogConfig(component.key).deployment_mode !== 'cluster' || !componentHasReplicaPaths(component)" /></a-form-item></a-grid-item><a-grid-item><a-form-item label="Namespace"><a-select v-model="catalogConfig(component.key).namespace" @change="setCatalogComponentNamespace(component, String($event))"><a-option v-for="row in componentNamespaceRows" :key="row.name" :value="row.name">{{ row.name }}</a-option></a-select></a-form-item></a-grid-item><a-grid-item><a-form-item :label="selfHostedDataComponentKeys.includes(component.key) ? '引擎版本' : '组件版本'"><a-select :model-value="componentSelectedVersion(component)" :loading="componentVersionLoading(component.key)" allow-search @popup-visible-change="(visible) => visible && loadComponentVersions(component)" @change="setComponentSelectedVersion(component, String($event))"><a-option v-for="option in componentVersionOptions(component)" :key="option.version" :value="option.version">{{ option.version }}{{ option.app_version ? ` · 应用 ${option.app_version}` : '' }}</a-option></a-select><small v-if="componentVersionHint(component)" class="field-help" :class="{ 'danger-text': componentVersionError(component.key) }">{{ componentVersionHint(component) }}</small></a-form-item></a-grid-item></a-grid></a-form>
		            <div class="component-advanced-picker"><div><strong>高级参数</strong><small>仅选择本环境确实需要覆盖的配置</small></div><a-select :model-value="componentAdvancedSelection(component)" multiple allow-clear :max-tag-count="3" placeholder="选择要配置的高级参数" @change="setComponentAdvancedSelection(component, $event)"><a-option v-for="option in componentAdvancedOptions(component)" :key="option.value" :value="option.value">{{ option.label }}</a-option></a-select></div>
		            <a-form v-if="componentAdvancedEnabled(component, 'timeout') || componentAdvancedEnabled(component, 'access')" :model="catalogConfig(component.key)" layout="vertical" class="component-advanced-fields"><a-grid :cols="3" :col-gap="14"><a-grid-item v-if="componentAdvancedEnabled(component, 'timeout')"><a-form-item label="部署超时（秒）"><a-input-number v-model="catalogConfig(component.key).timeout" :min="60" :max="7200" /></a-form-item></a-grid-item><template v-if="componentAdvancedEnabled(component, 'access')"><a-grid-item><a-form-item label="控制台域名"><a-input v-model="catalogConfig(component.key).domain" placeholder="可选，例如 console.example.com" /></a-form-item></a-grid-item><a-grid-item><a-form-item label="启用 TLS"><a-switch v-model="catalogConfig(component.key).tls" /></a-form-item></a-grid-item></template></a-grid></a-form>
		            <template v-if="component.key === 'higress'">
		              <a-divider orientation="left">NLB 入口安全</a-divider>
		              <a-alert type="warning" show-icon class="full-card">平台会在首次创建 Higress NLB 时绑定守护安全组，确保后续仍可调整安全组。可以由平台管理 80/443 入站规则、复用已有安全组，或同时使用两者。</a-alert>
		              <a-alert v-if="componentInstalled('higress')" type="info" show-icon class="full-card">旧环境的 NLB 如果最初未绑定任何安全组，AWS 不允许原地补绑；第一次应用本配置可能由控制器重建 NLB 并更换访问地址。请先降低 DNS TTL，并在变更日志中确认新地址后再切流。</a-alert>
		              <a-form :model="catalogConfig(component.key).nlb" layout="vertical"><a-grid :cols="4" :col-gap="14">
		                <a-grid-item :span="2"><a-form-item label="前端安全组模式" extra="已有安全组只能用于‘使用已有 VPC’的新 EKS；安全组无法跨 VPC 移动。"><a-radio-group v-model="catalogConfig(component.key).nlb.security_group_mode" type="button" @change="changeHigressNLBSecurityGroupMode(String($event))"><a-radio value="managed">平台管理</a-radio><a-radio value="custom" :disabled="!higressCanUseCustomSecurityGroups">仅已有安全组</a-radio><a-radio value="managed_plus_custom" :disabled="!higressCanUseCustomSecurityGroups">平台管理 + 已有安全组</a-radio></a-radio-group></a-form-item></a-grid-item>
		                <a-grid-item :span="2" v-if="higressUsesCustomSecurityGroups"><a-form-item label="已有安全组" extra="最多选择 4 个；默认安全组、EKS 集群安全组和其他环境的平台守护安全组禁止选择。"><a-select v-model="catalogConfig(component.key).nlb.security_group_ids" multiple allow-search allow-create :max-tag-count="2" :loading="loadingSecurityGroups" placeholder="选择或输入 sg-xxxxxxxx" @popup-visible-change="(visible) => visible && loadSecurityGroups()"><a-option v-for="group in securityGroups" :key="group.id" :value="group.id" :disabled="!group.selectable">{{ securityGroupLabel(group) }}</a-option></a-select><small v-if="securityGroupsError" class="field-help danger-text">{{ securityGroupsError }}</small></a-form-item></a-grid-item>
		                <a-grid-item><a-form-item label="NLB 网络类型" extra="Internal 仅提供 VPC 内部地址；已创建后修改类型会重建 NLB 并改变访问地址。"><a-radio-group v-model="catalogConfig(component.key).nlb.scheme" type="button"><a-radio value="internet-facing">公网</a-radio><a-radio value="internal">内网</a-radio></a-radio-group></a-form-item></a-grid-item>
		                <a-grid-item v-if="higressUsesManagedSecurityGroup"><a-form-item label="允许的入口端口" extra="不需要 HTTP 跳转或 ACME HTTP-01 时可只开放 443。"><a-checkbox-group v-model="catalogConfig(component.key).nlb.allowed_ports"><a-checkbox :value="80">HTTP 80</a-checkbox><a-checkbox :value="443">HTTPS 443</a-checkbox></a-checkbox-group></a-form-item></a-grid-item>
		                <a-grid-item :span="2" v-if="higressUsesManagedSecurityGroup"><a-form-item label="允许访问 NLB 的来源 IPv4 CIDR" extra="每行一个规范网段；使用 Cloudflare 时填写其官方出口网段。修改后平台只更新安全组规则，不重建 NLB。"><a-textarea :model-value="higressNLBAllowedCIDRsText" :auto-size="{ minRows: 4, maxRows: 10 }" placeholder="例如：203.0.113.10/32" @update:model-value="setHigressNLBAllowedCIDRs(String($event))" /></a-form-item></a-grid-item>
		                <a-grid-item v-if="higressUsesCustomSecurityGroups"><a-form-item label="自动维护后端规则" extra="关闭后必须自行允许 NLB 安全组访问 Pod/节点目标。"><a-switch v-model="catalogConfig(component.key).nlb.manage_backend_security_group_rules" /></a-form-item></a-grid-item>
		                <a-grid-item><a-form-item label="客户端源 IP 策略" extra="Local 可避免 kube-proxy SNAT，便于审计真实来源。"><a-select v-model="catalogConfig(component.key).nlb.external_traffic_policy"><a-option value="Local">Local（推荐）</a-option><a-option value="Cluster">Cluster</a-option></a-select></a-form-item></a-grid-item>
		                <a-grid-item><a-form-item label="TCP 空闲超时（秒）"><a-input-number v-model="catalogConfig(component.key).nlb.idle_timeout_seconds" :min="60" :max="6000" style="width:100%" /></a-form-item></a-grid-item>
		              </a-grid></a-form>
		              <a-alert v-if="existingEKSTarget" type="info" show-icon class="full-card">接入已有 EKS 时平台不修改集群级 AWS Load Balancer Controller，因此继续沿用集群现有 NLB 行为；自定义前端安全组模式仅用于平台托管的新 EKS。</a-alert>
		              <a-alert v-else-if="form.network.mode !== 'existing'" type="info" show-icon class="full-card">当前将新建 VPC，只能由平台在该 VPC 内创建入口安全组。若必须复用已有安全组，请在阶段1创建 EKS 前改为“使用已有 VPC”。</a-alert>
		              <a-alert v-if="higressSelectedSecurityGroupWarnings.length" type="warning" show-icon class="full-card">{{ higressSelectedSecurityGroupWarnings.join('；') }}</a-alert>
		              <a-alert v-if="store.currentEnvironment?.environment === 'prod' && higressNLBScheme === 'internet-facing' && higressUsesManagedSecurityGroup && higressNLBAllowedCIDRsText.includes('0.0.0.0/0')" type="error" show-icon class="full-card">生产公网 NLB 当前允许全互联网访问所选端口。若入口位于 CDN/WAF 后方，请改为其固定出口 CIDR。</a-alert>
		            </template>
		            <template v-if="component.key === 'clickvisual_stack'">
		              <a-divider orientation="left">一体化日志平台配置</a-divider>
		              <a-alert type="success" show-icon class="full-card">一次部署 Fluent Bit、Kafka、ClickHouse、ClickVisual 和独立 MySQL。平台会自动创建日志 Topic、ClickHouse 日志表和 ClickVisual 数据源；全部子组件统一部署在一个日志专用 Namespace。</a-alert>
		              <div class="log-stack-flow">
		                <span>Fluent Bit<small>采集 EKS 日志</small></span><i>→</i><span>Kafka<small>削峰与缓冲</small></span><i>→</i><span>ClickHouse<small>检索与保留</small></span><i>→</i><span>ClickVisual<small>查询控制台</small></span><b>MySQL<small>控制台元数据</small></b>
		              </div>
		              <a-form :model="catalogConfig(component.key).values" layout="vertical">
		                <a-grid :cols="4" :col-gap="14">
		                  <a-grid-item :span="2"><a-form-item label="日志系统 Namespace" extra="同一集群中的不同项目和环境必须使用不同名称。"><a-input :model-value="clickVisualNamespace" @change="setClickVisualNamespace(String($event))" /></a-form-item></a-grid-item>
		                  <a-grid-item><a-form-item label="配置预设"><a-select :model-value="clickVisualPreset" @change="applyClickVisualPreset(String($event))"><a-option value="test">测试 / 小规模</a-option><a-option value="production">生产 / 高可用</a-option><a-option value="custom">自定义</a-option></a-select></a-form-item></a-grid-item>
		                  <a-grid-item><a-form-item label="Kafka Broker"><a-select :model-value="Number(componentValue(component.key, 'kafka.replicas', 1))" @change="setComponentValue(component.key, 'kafka.replicas', Number($event))"><a-option :value="1">1（测试）</a-option><a-option :value="3">3（生产）</a-option></a-select></a-form-item></a-grid-item>
		                  <a-grid-item><a-form-item label="Kafka 分区"><a-input-number :model-value="Number(componentValue(component.key, 'kafka.partitions', 6))" :min="1" :max="96" @change="setComponentValue(component.key, 'kafka.partitions', Number($event))" /></a-form-item></a-grid-item>
		                  <a-grid-item><a-form-item label="Kafka 保留（小时）"><a-input-number :model-value="Number(componentValue(component.key, 'kafka.retentionHours', 24))" :min="1" :max="2160" @change="setComponentValue(component.key, 'kafka.retentionHours', Number($event))" /></a-form-item></a-grid-item>
		                  <a-grid-item><a-form-item label="ClickHouse 日志保留（天）"><a-input-number :model-value="Number(componentValue(component.key, 'clickhouse.retentionDays', 7))" :min="1" :max="3650" @change="setComponentValue(component.key, 'clickhouse.retentionDays', Number($event))" /></a-form-item></a-grid-item>
		                  <a-grid-item :span="4">
		                    <div class="log-collection-scope">
		                      <div class="log-collection-scope__header">
		                        <div><strong>Fluent Bit 日志采集范围</strong><small>{{ clickVisualCollectionSummary }}</small></div>
		                        <a-button size="small" :loading="loadingKubernetesServices" @click="loadKubernetesServices(true)"><icon-refresh />读取 EKS 服务</a-button>
		                      </div>
		                      <a-grid :cols="2" :col-gap="14" :row-gap="4">
		                        <a-grid-item>
		                          <a-form-item label="采集 Namespace" extra="留空表示采集全部 Namespace；可以选择 EKS 已发现项，也可以输入尚未创建的名称。">
		                            <a-select :model-value="clickVisualCollectionValues('includeNamespaces')" multiple allow-search allow-create allow-clear :max-tag-count="3" placeholder="全部 Namespace" @popup-visible-change="(visible) => visible && loadKubernetesServices()" @change="setClickVisualCollection('includeNamespaces', $event)">
		                              <a-option v-for="name in clickVisualCollectionNamespaceOptions" :key="name" :value="name">{{ name }}</a-option>
		                            </a-select>
		                          </a-form-item>
		                        </a-grid-item>
		                        <a-grid-item>
		                          <a-form-item label="排除 Namespace" extra="排除优先于采集；适合过滤 kube-system、日志系统自身等噪声。">
		                            <a-select :model-value="clickVisualCollectionValues('excludeNamespaces')" multiple allow-search allow-create allow-clear :max-tag-count="3" placeholder="不排除 Namespace" @popup-visible-change="(visible) => visible && loadKubernetesServices()" @change="setClickVisualCollection('excludeNamespaces', $event)">
		                              <a-option v-for="name in clickVisualCollectionNamespaceOptions" :key="name" :value="name">{{ name }}</a-option>
		                            </a-select>
		                          </a-form-item>
		                        </a-grid-item>
		                        <a-grid-item>
		                          <a-form-item label="采集服务 / 工作负载" extra="留空表示采集范围内全部服务；按常见 app 标签和 Pod 名称前缀识别。">
		                            <a-select :model-value="clickVisualCollectionValues('includeServices')" multiple allow-search allow-create allow-clear :max-tag-count="3" placeholder="全部服务" @popup-visible-change="(visible) => visible && loadKubernetesServices()" @change="setClickVisualCollection('includeServices', $event)">
		                              <a-option v-for="name in clickVisualCollectionServiceOptions" :key="name" :value="name">{{ name }}</a-option>
		                            </a-select>
		                          </a-form-item>
		                        </a-grid-item>
		                        <a-grid-item>
		                          <a-form-item label="排除服务 / 工作负载" extra="用于排除健康探针、测试服务或高频但无需保留的服务日志。">
		                            <a-select :model-value="clickVisualCollectionValues('excludeServices')" multiple allow-search allow-create allow-clear :max-tag-count="3" placeholder="不排除服务" @popup-visible-change="(visible) => visible && loadKubernetesServices()" @change="setClickVisualCollection('excludeServices', $event)">
		                              <a-option v-for="name in clickVisualCollectionServiceOptions" :key="name" :value="name">{{ name }}</a-option>
		                            </a-select>
		                          </a-form-item>
		                        </a-grid-item>
		                      </a-grid>
		                      <a-alert v-if="clickVisualCollectionConflicts.length" type="warning" show-icon>以下项目同时出现在采集和排除规则中，将按“排除优先”处理：{{ clickVisualCollectionConflicts.join('、') }}</a-alert>
		                      <div class="log-collection-scope__rule">执行顺序：采集 Namespace → 排除 Namespace → 采集服务 → 排除服务。服务名称最多 63 个字符，每组最多 100 项。</div>
		                    </div>
		                  </a-grid-item>
		                </a-grid>
		              </a-form>
		              <div class="log-stack-namespaces">
		                <div><small>Fluent Bit · Kafka · ClickHouse · ClickVisual · MySQL</small><strong>{{ clickVisualNamespace }}</strong></div>
		              </div>
		            </template>
		            <template v-if="component.key === 'efk_stack'">
		              <a-divider orientation="left">EFK 一体化日志系统</a-divider>
		              <a-alert type="success" show-icon class="full-card">一次部署 Elasticsearch、Fluentd 和 Kibana。平台会自动创建最小权限的 Fluentd 写入账号、日志保留策略和 Kibana 默认日志视图。</a-alert>
		              <div class="log-stack-flow efk-flow"><span>Fluentd<small>节点日志采集</small></span><i>→</i><span>Elasticsearch<small>索引与保留</small></span><i>→</i><span>Kibana<small>检索与可视化</small></span></div>
		              <a-form :model="catalogConfig(component.key).values" layout="vertical"><a-grid :cols="4" :col-gap="14">
		                <a-grid-item :span="2"><a-form-item label="日志系统 Namespace" extra="EFK 子组件统一部署到该 Namespace。"><a-input :model-value="efkNamespace" @update:model-value="setEFKNamespace(String($event))" /></a-form-item></a-grid-item>
		                <a-grid-item><a-form-item label="Elasticsearch 磁盘"><a-input :model-value="String(componentValue(component.key, 'elasticsearch.storage.size', '100Gi'))" @update:model-value="setComponentValue(component.key, 'elasticsearch.storage.size', String($event))" /></a-form-item></a-grid-item>
		                <a-grid-item><a-form-item label="StorageClass"><a-input :model-value="String(componentValue(component.key, 'elasticsearch.storage.className', 'gp3'))" @update:model-value="setComponentValue(component.key, 'elasticsearch.storage.className', String($event))" /></a-form-item></a-grid-item>
		                <a-grid-item><a-form-item label="日志保留（天）"><a-input-number :model-value="Number(componentValue(component.key, 'elasticsearch.retentionDays', 7))" :min="1" :max="3650" @update:model-value="setComponentValue(component.key, 'elasticsearch.retentionDays', Number($event))" /></a-form-item></a-grid-item>
		                <a-grid-item><a-form-item label="JVM Heap 参数" extra="例如 -Xms1g -Xmx1g；Xms 与 Xmx 必须相同，且不能超过容器内存上限的 50%"><a-input :model-value="String(componentValue(component.key, 'elasticsearch.javaOpts', '-Xms1g -Xmx1g'))" @update:model-value="setComponentValue(component.key, 'elasticsearch.javaOpts', String($event))" /></a-form-item></a-grid-item>
		                <a-grid-item :span="4"><div class="log-collection-scope">
		                  <div class="log-collection-scope__header"><div><strong>Fluentd 日志采集范围</strong><small>{{ logCollectionSummary('efk_stack') }}</small></div><a-button size="small" :loading="loadingKubernetesServices" @click="loadKubernetesServices(true)"><icon-refresh />读取 EKS 服务</a-button></div>
		                  <a-grid :cols="2" :col-gap="14" :row-gap="4">
		                    <a-grid-item><a-form-item label="采集 Namespace" extra="留空表示全部 Namespace。"><a-select :model-value="logCollectionValues('efk_stack', 'includeNamespaces')" multiple allow-search allow-create allow-clear :max-tag-count="3" placeholder="全部 Namespace" @popup-visible-change="(visible) => visible && loadKubernetesServices()" @change="setLogCollection('efk_stack', 'includeNamespaces', $event)"><a-option v-for="name in clickVisualCollectionNamespaceOptions" :key="name" :value="name">{{ name }}</a-option></a-select></a-form-item></a-grid-item>
		                    <a-grid-item><a-form-item label="排除 Namespace" extra="排除规则优先。"><a-select :model-value="logCollectionValues('efk_stack', 'excludeNamespaces')" multiple allow-search allow-create allow-clear :max-tag-count="3" placeholder="不排除 Namespace" @popup-visible-change="(visible) => visible && loadKubernetesServices()" @change="setLogCollection('efk_stack', 'excludeNamespaces', $event)"><a-option v-for="name in clickVisualCollectionNamespaceOptions" :key="name" :value="name">{{ name }}</a-option></a-select></a-form-item></a-grid-item>
		                    <a-grid-item><a-form-item label="采集服务 / 工作负载" extra="留空表示采集范围内全部服务。"><a-select :model-value="logCollectionValues('efk_stack', 'includeServices')" multiple allow-search allow-create allow-clear :max-tag-count="3" placeholder="全部服务" @popup-visible-change="(visible) => visible && loadKubernetesServices()" @change="setLogCollection('efk_stack', 'includeServices', $event)"><a-option v-for="name in logCollectionServiceOptions('efk_stack')" :key="name" :value="name">{{ name }}</a-option></a-select></a-form-item></a-grid-item>
		                    <a-grid-item><a-form-item label="排除服务 / 工作负载" extra="可用于过滤高频噪声服务。"><a-select :model-value="logCollectionValues('efk_stack', 'excludeServices')" multiple allow-search allow-create allow-clear :max-tag-count="3" placeholder="不排除服务" @popup-visible-change="(visible) => visible && loadKubernetesServices()" @change="setLogCollection('efk_stack', 'excludeServices', $event)"><a-option v-for="name in logCollectionServiceOptions('efk_stack')" :key="name" :value="name">{{ name }}</a-option></a-select></a-form-item></a-grid-item>
		                  </a-grid>
		                  <a-alert v-if="logCollectionConflicts('efk_stack').length" type="warning" show-icon>以下项目同时出现在采集和排除规则中，将按“排除优先”处理：{{ logCollectionConflicts('efk_stack').join('、') }}</a-alert>
		                  <div class="log-collection-scope__rule">执行顺序：采集 Namespace → 排除 Namespace → 采集服务 → 排除服务。</div>
		                </div></a-grid-item>
		              </a-grid></a-form>
		              <div class="log-stack-namespaces"><div><small>Elasticsearch · Fluentd · Kibana</small><strong>{{ efkNamespace }}</strong></div></div>
		            </template>
		            <template v-if="component.key === 'opentelemetry_collector'">
		              <a-divider orientation="left">OpenTelemetry 统一采集与链路追踪</a-divider>
		              <a-alert type="success" show-icon class="full-card"><strong>Agent DaemonSet → Collector Gateway → Jaeger / Prometheus / Loki</strong>。OpenTelemetry 统一采集、加工与转发，Jaeger 默认负责 Trace 持久化、检索和链路面板；Tempo 与 Elasticsearch 为可选出口。业务服务使用 <code>{{ openTelemetryEndpoint }}:4317</code>（gRPC）或 <code>{{ openTelemetryEndpoint }}:4318</code>（HTTP）。</a-alert>
		              <a-form :model="catalogConfig(component.key).values" layout="vertical">
		                <a-grid :cols="4" :col-gap="14">
		                  <a-grid-item :span="4"><a-form-item label="资源预设"><a-radio-group :model-value="openTelemetryPreset" type="button" @change="applyOpenTelemetryPreset(String($event))"><a-radio value="test">最小测试版</a-radio><a-radio value="production">生产推荐版</a-radio><a-radio value="custom" disabled>自定义</a-radio></a-radio-group></a-form-item></a-grid-item>
		                  <a-grid-item><a-form-item label="节点 Agent"><a-switch :model-value="Boolean(componentValue(component.key, 'agent.enabled', true))" @change="setComponentValue(component.key, 'agent.enabled', Boolean($event))" /></a-form-item></a-grid-item>
		                  <a-grid-item><a-form-item label="采集容器日志"><a-switch :disabled="!Boolean(componentValue(component.key, 'agent.enabled', true))" :model-value="Boolean(componentValue(component.key, 'agent.logs.enabled', true))" @change="setComponentValue(component.key, 'agent.logs.enabled', Boolean($event))" /></a-form-item></a-grid-item>
		                  <a-grid-item><a-form-item label="采集节点 / Pod 指标"><a-switch :disabled="!Boolean(componentValue(component.key, 'agent.enabled', true))" :model-value="Boolean(componentValue(component.key, 'agent.metrics.enabled', true))" @change="setComponentValue(component.key, 'agent.metrics.enabled', Boolean($event))" /></a-form-item></a-grid-item>
		                  <a-grid-item><a-form-item label="Trace → Jaeger（默认）"><a-switch :model-value="Boolean(componentValue(component.key, 'destinations.jaeger.enabled', true))" @change="setOpenTelemetryDestinationEnabled('jaeger', Boolean($event))" /></a-form-item></a-grid-item>
		                  <a-grid-item><a-form-item label="Trace → Tempo（可选）"><a-switch :model-value="Boolean(componentValue(component.key, 'destinations.tempo.enabled', false))" @change="setOpenTelemetryDestinationEnabled('tempo', Boolean($event))" /></a-form-item></a-grid-item>
		                  <a-grid-item><a-form-item label="Metric → Prometheus"><a-switch :model-value="Boolean(componentValue(component.key, 'destinations.prometheus.enabled', true))" @change="setComponentValue(component.key, 'destinations.prometheus.enabled', Boolean($event))" /></a-form-item></a-grid-item>
		                  <a-grid-item><a-form-item label="Log → Loki"><a-switch :model-value="Boolean(componentValue(component.key, 'destinations.loki.enabled', true))" @change="setComponentValue(component.key, 'destinations.loki.enabled', Boolean($event))" /></a-form-item></a-grid-item>
		                  <a-grid-item><a-form-item label="独立 Elasticsearch 存储" extra="独立于 EFK；启用后自动保存 OTel 日志和 Jaeger Trace。"><a-switch :model-value="openTelemetryElasticsearchEnabled" @change="setOpenTelemetryElasticsearchEnabled(Boolean($event))" /></a-form-item></a-grid-item>
		                  <a-grid-item v-if="openTelemetryElasticsearchEnabled" :span="4"><div class="log-collection-scope">
		                    <div class="log-collection-scope__header"><div><strong>OpenTelemetry 专用 Elasticsearch</strong><small>独立 Helm Release、密码、StatefulSet 和 PVC；不读取或修改 EFK 数据。</small></div><a-tag color="purple">{{ openTelemetryElasticsearchMode === 'cluster' ? `${openTelemetryElasticsearchReplicas} 节点集群` : '单节点' }}</a-tag></div>
		                    <a-grid :cols="4" :col-gap="14" :row-gap="4">
		                      <a-grid-item><a-form-item label="部署模式" extra="首次部署后锁定，避免直接变更集群拓扑造成数据风险。"><a-radio-group :disabled="openTelemetryElasticsearchActual" :model-value="openTelemetryElasticsearchMode" type="button" @change="setOpenTelemetryElasticsearchMode(String($event))"><a-radio value="standalone">单机</a-radio><a-radio value="cluster">集群</a-radio></a-radio-group></a-form-item></a-grid-item>
		                      <a-grid-item><a-form-item label="ES 节点数" extra="首次部署可选 3、5、7、9 个节点。"><a-select :disabled="openTelemetryElasticsearchActual || openTelemetryElasticsearchMode !== 'cluster'" :model-value="openTelemetryElasticsearchReplicas" @change="setOpenTelemetryElasticsearchReplicas(Number($event))"><a-option v-for="count in [3, 5, 7, 9]" :key="count" :value="count">{{ count }} 节点</a-option></a-select></a-form-item></a-grid-item>
		                      <a-grid-item><a-form-item label="Elasticsearch 版本"><a-select :model-value="String(componentValue(component.key, 'elasticsearch.image.tag', '8.19.17'))" @change="setComponentValue(component.key, 'elasticsearch.image.tag', String($event))"><a-option v-for="version in openTelemetryElasticsearchVersions" :key="version" :value="version">{{ version }}</a-option></a-select></a-form-item></a-grid-item>
		                      <a-grid-item><a-form-item label="安装 Namespace"><a-input :model-value="String(catalogConfig(component.key).namespace || 'monitoring')" disabled /></a-form-item></a-grid-item>
		                      <a-grid-item :span="2"><a-form-item label="集群内地址"><a-input :model-value="openTelemetryElasticsearchEndpoint" disabled /></a-form-item></a-grid-item>
		                      <a-grid-item><a-form-item label="StorageClass"><a-input :disabled="openTelemetryElasticsearchActual" :model-value="String(componentValue(component.key, 'elasticsearch.storage.className', 'gp3'))" @change="setComponentValue(component.key, 'elasticsearch.storage.className', String($event).trim())" /></a-form-item></a-grid-item>
		                      <a-grid-item><a-form-item label="单节点初始磁盘" extra="部署后使用下方在线扩容，避免修改 StatefulSet 不可变字段。"><a-input :disabled="openTelemetryElasticsearchActual" :model-value="String(componentValue(component.key, 'elasticsearch.storage.initialSize', '50Gi'))" @change="setComponentValue(component.key, 'elasticsearch.storage.initialSize', String($event).trim())" /></a-form-item></a-grid-item>
		                      <a-grid-item :span="2"><a-form-item label="JVM Heap"><a-input :model-value="String(componentValue(component.key, 'elasticsearch.javaOpts', '-Xms1g -Xmx1g'))" @change="setComponentValue(component.key, 'elasticsearch.javaOpts', String($event).trim())" /></a-form-item></a-grid-item>
		                      <a-grid-item><a-form-item label="请求 CPU"><a-input :model-value="String(componentValue(component.key, 'elasticsearch.resources.requests.cpu', '500m'))" @change="setComponentValue(component.key, 'elasticsearch.resources.requests.cpu', String($event).trim())" /></a-form-item></a-grid-item>
		                      <a-grid-item><a-form-item label="请求内存"><a-input :model-value="String(componentValue(component.key, 'elasticsearch.resources.requests.memory', '2Gi'))" @change="setComponentValue(component.key, 'elasticsearch.resources.requests.memory', String($event).trim())" /></a-form-item></a-grid-item>
		                      <a-grid-item><a-form-item label="限制 CPU"><a-input :model-value="String(componentValue(component.key, 'elasticsearch.resources.limits.cpu', '2'))" @change="setComponentValue(component.key, 'elasticsearch.resources.limits.cpu', String($event).trim())" /></a-form-item></a-grid-item>
		                      <a-grid-item><a-form-item label="限制内存"><a-input :model-value="String(componentValue(component.key, 'elasticsearch.resources.limits.memory', '4Gi'))" @change="setComponentValue(component.key, 'elasticsearch.resources.limits.memory', String($event).trim())" /></a-form-item></a-grid-item>
		                      <a-grid-item><a-form-item label="卸载时保留 PVC"><a-switch :model-value="Boolean(componentValue(component.key, 'elasticsearch.storage.retainOnDelete', true))" @change="setComponentValue(component.key, 'elasticsearch.storage.retainOnDelete', Boolean($event))" /></a-form-item></a-grid-item>
		                    </a-grid>
		                    <a-alert type="warning" show-icon>集群模式每个节点都会创建独立 PVC；关闭该开关会卸载专用 ES，但默认保留数据盘。正式环境建议 3 节点起步，并确保平台节点组有足够内存。</a-alert>
		                    <div class="managed-storage-toolbar"><div><strong>Elasticsearch 数据盘</strong><small>每个 ES 节点一块独立 PVC，仅支持不中断在线扩容。</small></div><a-button :loading="loadingOpenTelemetryStorage" :disabled="dirty || environmentBusy || !baseReady" @click="loadOpenTelemetryStorage"><icon-refresh />刷新容量</a-button></div>
		                    <a-table :data="openTelemetryElasticsearchStorageItems" :loading="loadingOpenTelemetryStorage" :pagination="false" size="small" row-key="pvc_name">
		                      <template #columns>
		                        <a-table-column title="PVC"><template #cell="{ record }"><span class="managed-storage-pvc">{{ record.namespace }}/{{ record.pvc_name }}</span></template></a-table-column>
		                        <a-table-column title="请求 / 实际"><template #cell="{ record }">{{ record.requested || '—' }} / {{ record.capacity || '—' }}</template></a-table-column>
		                        <a-table-column title="存储类"><template #cell="{ record }">{{ record.storage_class }}<a-tag v-if="record.allow_expansion" color="green" size="small">可在线扩容</a-tag></template></a-table-column>
		                        <a-table-column title="状态"><template #cell="{ record }"><a-tag :color="record.active && record.phase === 'Bound' ? 'green' : 'gray'">{{ record.active ? record.phase : '已保留旧盘' }}</a-tag></template></a-table-column>
		                        <a-table-column title="操作" :width="100"><template #cell="{ record }"><a-button v-if="record.active" size="mini" type="primary" :disabled="!record.allow_expansion || !store.canDeploy || environmentBusy" @click="openManagedStorageResize(record, 'expand')">扩容</a-button><span v-else>保留</span></template></a-table-column>
		                      </template>
		                      <template #empty><a-empty description="部署成功后点击“刷新容量”查看专用 Elasticsearch PVC" /></template>
		                    </a-table>
		                  </div></a-grid-item>
		                  <a-grid-item :span="4"><div class="log-collection-scope">
		                    <div class="log-collection-scope__header"><div><strong>Agent 容器日志采集范围</strong><small>{{ logCollectionSummary('opentelemetry_collector') }}</small></div><a-button size="small" :loading="loadingKubernetesServices" @click="loadKubernetesServices(true)"><icon-refresh />读取 EKS 服务</a-button></div>
		                    <a-grid :cols="2" :col-gap="14" :row-gap="4">
		                      <a-grid-item><a-form-item label="采集 Namespace"><a-select :model-value="logCollectionValues('opentelemetry_collector', 'includeNamespaces')" multiple allow-search allow-create allow-clear :max-tag-count="3" placeholder="全部 Namespace" @popup-visible-change="(visible) => visible && loadKubernetesServices()" @change="setLogCollection('opentelemetry_collector', 'includeNamespaces', $event)"><a-option v-for="name in clickVisualCollectionNamespaceOptions" :key="name" :value="name">{{ name }}</a-option></a-select></a-form-item></a-grid-item>
		                      <a-grid-item><a-form-item label="排除 Namespace"><a-select :model-value="logCollectionValues('opentelemetry_collector', 'excludeNamespaces')" multiple allow-search allow-create allow-clear :max-tag-count="3" placeholder="不排除 Namespace" @popup-visible-change="(visible) => visible && loadKubernetesServices()" @change="setLogCollection('opentelemetry_collector', 'excludeNamespaces', $event)"><a-option v-for="name in clickVisualCollectionNamespaceOptions" :key="name" :value="name">{{ name }}</a-option></a-select></a-form-item></a-grid-item>
		                      <a-grid-item><a-form-item label="采集服务 / 工作负载"><a-select :model-value="logCollectionValues('opentelemetry_collector', 'includeServices')" multiple allow-search allow-create allow-clear :max-tag-count="3" placeholder="全部服务" @popup-visible-change="(visible) => visible && loadKubernetesServices()" @change="setLogCollection('opentelemetry_collector', 'includeServices', $event)"><a-option v-for="name in logCollectionServiceOptions('opentelemetry_collector')" :key="name" :value="name">{{ name }}</a-option></a-select></a-form-item></a-grid-item>
		                      <a-grid-item><a-form-item label="排除服务 / 工作负载"><a-select :model-value="logCollectionValues('opentelemetry_collector', 'excludeServices')" multiple allow-search allow-create allow-clear :max-tag-count="3" placeholder="不排除服务" @popup-visible-change="(visible) => visible && loadKubernetesServices()" @change="setLogCollection('opentelemetry_collector', 'excludeServices', $event)"><a-option v-for="name in logCollectionServiceOptions('opentelemetry_collector')" :key="name" :value="name">{{ name }}</a-option></a-select></a-form-item></a-grid-item>
		                    </a-grid>
		                    <a-alert v-if="logCollectionConflicts('opentelemetry_collector').length" type="warning" show-icon>采集和排除规则存在重复项，将按排除优先处理：{{ logCollectionConflicts('opentelemetry_collector').join('、') }}</a-alert>
		                  </div></a-grid-item>
		                  <a-grid-item><a-form-item label="请求 CPU"><a-input :model-value="String(componentValue(component.key, 'resources.requests.cpu', '200m'))" @change="setComponentValue(component.key, 'resources.requests.cpu', String($event).trim())" /></a-form-item></a-grid-item>
		                  <a-grid-item><a-form-item label="请求内存"><a-input :model-value="String(componentValue(component.key, 'resources.requests.memory', '256Mi'))" @change="setComponentValue(component.key, 'resources.requests.memory', String($event).trim())" /></a-form-item></a-grid-item>
		                  <a-grid-item><a-form-item label="限制 CPU"><a-input :model-value="String(componentValue(component.key, 'resources.limits.cpu', '1'))" @change="setComponentValue(component.key, 'resources.limits.cpu', String($event).trim())" /></a-form-item></a-grid-item>
		                  <a-grid-item><a-form-item label="限制内存"><a-input :model-value="String(componentValue(component.key, 'resources.limits.memory', '1Gi'))" @change="setComponentValue(component.key, 'resources.limits.memory', String($event).trim())" /></a-form-item></a-grid-item>
		                  <a-grid-item><a-form-item label="StorageClass" extra="部署后不可修改；需要迁移存储类时应新建 Collector。"><a-input :disabled="componentActual(component)" :model-value="String(componentValue(component.key, 'storage.className', 'gp3'))" @change="setOpenTelemetryStorageClass(String($event))" /></a-form-item></a-grid-item>
		                  <a-grid-item><a-form-item label="单副本初始磁盘" extra="部署后请使用下方在线扩容，不直接修改初始容量。"><a-input :disabled="componentActual(component)" :model-value="String(componentValue(component.key, 'storage.initialSize', '10Gi'))" @change="setOpenTelemetryInitialSize(String($event))" /></a-form-item></a-grid-item>
		                  <a-grid-item><a-form-item label="持久队列容量（批次）" extra="控制发送队列最多保留的批次数，不等于磁盘 GiB。"><a-input-number :model-value="Number(componentValue(component.key, 'storage.queueSize', 1000))" :min="1" :max="1000000" style="width:100%" @change="setOpenTelemetryQueueSize(Number($event))" /></a-form-item></a-grid-item>
		                  <a-grid-item><a-form-item label="卸载时保留 PVC"><a-switch :model-value="Boolean(componentValue(component.key, 'storage.retainOnDelete', true))" @change="setOpenTelemetryRetainOnDelete(Boolean($event))" /></a-form-item></a-grid-item>
		                </a-grid>
		              </a-form>
		              <a-alert type="info" show-icon class="full-card">Collector PVC 只保存发送队列/WAL，用于下游短暂异常后的断点续传；Jaeger、Tempo、Prometheus、Loki、Elasticsearch 各自保存可查询的长期数据。启用 OTel 日志 Agent 后平台会自动停用同环境的 Alloy，避免 Loki 重复写入。</a-alert>
		              <a-collapse class="otel-access-example"><a-collapse-item key="env" header="应用接入环境变量（Go / Java / Node.js 通用）"><pre>{{ openTelemetryEnvironmentExample }}</pre></a-collapse-item></a-collapse>
		              <a-divider orientation="left">运行中队列存储</a-divider>
		              <div class="managed-storage-toolbar"><div><strong>Collector WAL PVC</strong><small>集群模式每个副本一块独立磁盘；支持不中断在线扩容，不提供自动缩容。</small></div><a-button :loading="loadingOpenTelemetryStorage" :disabled="dirty || environmentBusy || !baseReady" @click="loadOpenTelemetryStorage"><icon-refresh />刷新容量</a-button></div>
		              <a-table :data="openTelemetryWALStorageItems" :loading="loadingOpenTelemetryStorage" :pagination="false" size="small" row-key="pvc_name">
		                <template #columns>
		                  <a-table-column title="PVC"><template #cell="{ record }"><span class="managed-storage-pvc">{{ record.namespace }}/{{ record.pvc_name }}</span></template></a-table-column>
		                  <a-table-column title="请求 / 实际"><template #cell="{ record }">{{ record.requested || '—' }} / {{ record.capacity || '—' }}</template></a-table-column>
		                  <a-table-column title="存储类"><template #cell="{ record }">{{ record.storage_class }}<a-tag v-if="record.allow_expansion" color="green" size="small">可在线扩容</a-tag></template></a-table-column>
		                  <a-table-column title="状态"><template #cell="{ record }"><a-tag :color="record.active && record.phase === 'Bound' ? 'green' : 'gray'">{{ record.active ? record.phase : '已保留旧盘' }}</a-tag></template></a-table-column>
		                  <a-table-column title="操作" :width="100"><template #cell="{ record }"><a-button v-if="record.active" size="mini" type="primary" :disabled="!record.allow_expansion || !store.canDeploy || environmentBusy" @click="openManagedStorageResize(record, 'expand')">扩容</a-button><span v-else>保留</span></template></a-table-column>
		                </template>
		                <template #empty><a-empty description="部署成功后点击“刷新容量”查看 Collector WAL PVC" /></template>
		              </a-table>
		            </template>
		            <template v-if="component.key === 'jaeger'">
		              <a-divider orientation="left">Jaeger 链路存储与查询面板</a-divider>
		              <a-alert type="success" show-icon class="full-card"><strong>OpenTelemetry → Jaeger Collector → Trace Storage → Jaeger Query/UI</strong> 已完整对接。面板通过 Basic Auth 保护，部署成功后可在“资源与访问”查看地址、用户名和密码。</a-alert>
		              <a-form :model="catalogConfig(component.key).values" layout="vertical"><a-grid :cols="4" :col-gap="14">
		                <a-grid-item :span="2"><a-form-item label="Trace 存储后端"><a-radio-group :model-value="String(componentValue(component.key, 'storage.backend', 'badger'))" type="button" @change="setJaegerStorageBackend(String($event))"><a-radio value="badger">Badger 持久化（轻量）</a-radio><a-radio value="elasticsearch">Elasticsearch（生产）</a-radio></a-radio-group></a-form-item></a-grid-item>
		                <a-grid-item><a-form-item label="UI 登录用户名"><a-input :model-value="String(componentValue(component.key, 'basicAuth.username', 'admin'))" @change="setComponentValue(component.key, 'basicAuth.username', String($event).trim())" /></a-form-item></a-grid-item>
		                <a-grid-item><a-form-item label="Jaeger UI Service"><a-input model-value="jaeger:80" disabled /></a-form-item></a-grid-item>
		                <template v-if="jaegerStorageBackend === 'badger'">
		                  <a-grid-item><a-form-item label="StorageClass"><a-input :disabled="componentActual(component)" :model-value="String(componentValue(component.key, 'storage.className', 'gp3'))" @change="setComponentValue(component.key, 'storage.className', String($event).trim())" /></a-form-item></a-grid-item>
		                  <a-grid-item><a-form-item label="Trace 磁盘（GiB）"><a-input-number :disabled="componentActual(component)" :model-value="jaegerDiskGiB" :min="1" :max="16384" style="width:100%" @change="setJaegerDiskGiB(Number($event))" /></a-form-item></a-grid-item>
		                  <a-grid-item><a-form-item label="Trace 保留（天）"><a-input-number :model-value="jaegerRetentionDays" :min="1" :max="3650" style="width:100%" @change="setJaegerRetentionDays(Number($event))" /></a-form-item></a-grid-item>
		                  <a-grid-item><a-form-item label="卸载时保留 PVC"><a-switch :model-value="Boolean(componentValue(component.key, 'storage.retainOnDelete', true))" @change="setComponentValue(component.key, 'storage.retainOnDelete', Boolean($event))" /></a-form-item></a-grid-item>
		                </template>
		                <template v-else>
		                  <a-grid-item :span="2"><a-form-item label="Elasticsearch 地址"><a-input :model-value="String(componentValue(component.key, 'storage.elasticsearch.endpoint', jaegerElasticsearchEndpoint))" disabled /></a-form-item></a-grid-item>
		                  <a-grid-item><a-form-item label="索引前缀"><a-input :model-value="String(componentValue(component.key, 'storage.elasticsearch.indexPrefix', 'jaeger'))" @change="setComponentValue(component.key, 'storage.elasticsearch.indexPrefix', String($event).trim())" /></a-form-item></a-grid-item>
		                  <a-grid-item><a-form-item label="索引副本"><a-input-number :model-value="Number(componentValue(component.key, 'storage.elasticsearch.replicas', 0))" :min="0" :max="10" @change="setComponentValue(component.key, 'storage.elasticsearch.replicas', Number($event))" /></a-form-item></a-grid-item>
		                  <a-grid-item><a-form-item label="Trace 保留（天）"><a-input-number :model-value="Number(componentValue(component.key, 'storage.elasticsearch.retentionDays', 30))" :min="1" :max="3650" style="width:100%" @change="setComponentValue(component.key, 'storage.elasticsearch.retentionDays', Number($event))" /></a-form-item></a-grid-item>
		                  <a-grid-item><a-form-item label="索引清理计划"><a-input :model-value="String(componentValue(component.key, 'storage.elasticsearch.indexCleaner.schedule', '17 2 * * *'))" @change="setComponentValue(component.key, 'storage.elasticsearch.indexCleaner.schedule', String($event).trim())" /></a-form-item></a-grid-item>
		                </template>
		                <a-grid-item><a-form-item label="请求 CPU"><a-input :model-value="String(componentValue(component.key, 'resources.requests.cpu', '250m'))" @change="setComponentValue(component.key, 'resources.requests.cpu', String($event).trim())" /></a-form-item></a-grid-item>
		                <a-grid-item><a-form-item label="请求内存"><a-input :model-value="String(componentValue(component.key, 'resources.requests.memory', '512Mi'))" @change="setComponentValue(component.key, 'resources.requests.memory', String($event).trim())" /></a-form-item></a-grid-item>
		                <a-grid-item><a-form-item label="限制 CPU"><a-input :model-value="String(componentValue(component.key, 'resources.limits.cpu', '2'))" @change="setComponentValue(component.key, 'resources.limits.cpu', String($event).trim())" /></a-form-item></a-grid-item>
		                <a-grid-item><a-form-item label="限制内存"><a-input :model-value="String(componentValue(component.key, 'resources.limits.memory', '2Gi'))" @change="setComponentValue(component.key, 'resources.limits.memory', String($event).trim())" /></a-form-item></a-grid-item>
		              </a-grid></a-form>
		              <a-alert v-if="jaegerStorageBackend === 'badger'" type="warning" show-icon class="full-card">Badger 适合测试和中小规模单副本场景；它不能水平扩展。生产集群请切换 Elasticsearch，平台会启用 OpenTelemetry 专用 Elasticsearch。</a-alert>
		              <a-alert v-else type="info" show-icon class="full-card">Elasticsearch 模式支持 Jaeger 多副本和集中持久化；该实例独立于 EFK，使用 <code>jaeger-*</code> 索引，并按上方保留天数定时清理旧 Trace。</a-alert>
		            </template>
		            <template v-if="component.key === 'tempo'">
		              <a-divider orientation="left">Tempo 链路存储</a-divider>
		              <a-alert type="info" show-icon class="full-card">Tempo 接收 Collector Gateway 转发的 OTLP Trace，并自动接入 Grafana。当前采用单体 Tempo + EBS PVC，适合测试和中小规模生产；超大规模生产建议改用 tempo-distributed + S3。</a-alert>
		              <a-form :model="catalogConfig(component.key).values" layout="vertical"><a-grid :cols="4" :col-gap="14">
		                <a-grid-item><a-form-item label="启用持久化"><a-switch :model-value="Boolean(componentValue(component.key, 'persistence.enabled', true))" @change="setComponentValue(component.key, 'persistence.enabled', Boolean($event))" /></a-form-item></a-grid-item>
		                <a-grid-item><a-form-item label="StorageClass"><a-input :disabled="componentActual(component)" :model-value="String(componentValue(component.key, 'persistence.storageClassName', 'gp3'))" @change="setComponentValue(component.key, 'persistence.storageClassName', String($event).trim())" /></a-form-item></a-grid-item>
		                <a-grid-item><a-form-item label="Trace 磁盘（GiB）"><a-input-number :model-value="tempoDiskGiB" :min="1" :max="16384" style="width:100%" @change="setTempoDiskGiB(Number($event))" /></a-form-item></a-grid-item>
		                <a-grid-item><a-form-item label="Trace 保留（天）"><a-input-number :model-value="tempoRetentionDays" :min="1" :max="3650" style="width:100%" @change="setTempoRetentionDays(Number($event))" /></a-form-item></a-grid-item>
		                <a-grid-item><a-form-item label="生成服务拓扑指标"><a-switch :model-value="Boolean(componentValue(component.key, 'tempo.metricsGenerator.enabled', true))" @change="setComponentValue(component.key, 'tempo.metricsGenerator.enabled', Boolean($event))" /></a-form-item></a-grid-item>
		                <a-grid-item><a-form-item label="请求 CPU"><a-input :model-value="String(componentValue(component.key, 'tempo.resources.requests.cpu', '250m'))" @change="setComponentValue(component.key, 'tempo.resources.requests.cpu', String($event).trim())" /></a-form-item></a-grid-item>
		                <a-grid-item><a-form-item label="请求内存"><a-input :model-value="String(componentValue(component.key, 'tempo.resources.requests.memory', '512Mi'))" @change="setComponentValue(component.key, 'tempo.resources.requests.memory', String($event).trim())" /></a-form-item></a-grid-item>
		                <a-grid-item><a-form-item label="限制内存"><a-input :model-value="String(componentValue(component.key, 'tempo.resources.limits.memory', '2Gi'))" @change="setComponentValue(component.key, 'tempo.resources.limits.memory', String($event).trim())" /></a-form-item></a-grid-item>
		              </a-grid></a-form>
		            </template>
		            <template v-if="selfHostedDataComponentKeys.includes(component.key)">
		              <a-divider orientation="left">数据库/中间件运行参数</a-divider>
		              <a-form :model="catalogConfig(component.key).values" layout="vertical"><a-grid :cols="4" :col-gap="14">
		                <a-grid-item><a-form-item label="用户名"><a-input :model-value="String(componentValue(component.key, 'auth.username', ''))" @change="setDataServiceUsername(component.key, String($event))" /></a-form-item></a-grid-item>
		                <a-grid-item v-if="['mysql','mongodb'].includes(component.key)"><a-form-item label="默认数据库"><a-input :model-value="String(componentValue(component.key, 'auth.database', 'app'))" @change="setComponentValue(component.key, 'auth.database', String($event).trim())" /></a-form-item></a-grid-item>
		                <a-grid-item><a-form-item label="启用持久化"><a-switch :model-value="Boolean(componentValue(component.key, 'storage.enabled', true))" @change="setComponentValue(component.key, 'storage.enabled', Boolean($event))" /></a-form-item></a-grid-item>
		                <template v-if="componentValue(component.key, 'storage.enabled', true)"><a-grid-item><a-form-item label="StorageClass"><a-input :model-value="String(componentValue(component.key, 'storage.className', 'gp3'))" @change="setComponentValue(component.key, 'storage.className', String($event).trim())" /></a-form-item></a-grid-item><a-grid-item><a-form-item label="单副本磁盘"><a-input :model-value="String(componentValue(component.key, 'storage.size', '20Gi'))" @change="setComponentValue(component.key, 'storage.size', String($event).trim())" /></a-form-item></a-grid-item><a-grid-item><a-form-item label="卸载时保留 PVC"><a-switch :model-value="Boolean(componentValue(component.key, 'storage.retainOnDelete', true))" @change="setComponentValue(component.key, 'storage.retainOnDelete', Boolean($event))" /></a-form-item></a-grid-item></template>
		                <template v-if="componentAdvancedEnabled(component, 'resources')"><a-grid-item><a-form-item label="请求 CPU"><a-input :model-value="String(componentValue(component.key, 'resources.requests.cpu', '250m'))" @change="setComponentValue(component.key, 'resources.requests.cpu', String($event).trim())" /></a-form-item></a-grid-item><a-grid-item><a-form-item label="请求内存"><a-input :model-value="String(componentValue(component.key, 'resources.requests.memory', '512Mi'))" @change="setComponentValue(component.key, 'resources.requests.memory', String($event).trim())" /></a-form-item></a-grid-item><a-grid-item><a-form-item label="限制 CPU"><a-input :model-value="String(componentValue(component.key, 'resources.limits.cpu', '2'))" @change="setComponentValue(component.key, 'resources.limits.cpu', String($event).trim())" /></a-form-item></a-grid-item><a-grid-item><a-form-item label="限制内存"><a-input :model-value="String(componentValue(component.key, 'resources.limits.memory', '2Gi'))" @change="setComponentValue(component.key, 'resources.limits.memory', String($event).trim())" /></a-form-item></a-grid-item></template>
		                <template v-if="componentAdvancedEnabled(component, 'tuning')"><a-grid-item v-if="component.key === 'mysql'"><a-form-item label="最大连接数"><a-input-number :model-value="Number(componentValue(component.key, 'settings.maxConnections', 500))" :min="10" :max="100000" @change="setComponentValue(component.key, 'settings.maxConnections', Number($event))" /></a-form-item></a-grid-item><a-grid-item v-if="component.key === 'redis'"><a-form-item label="AOF 持久化"><a-switch :model-value="Boolean(componentValue(component.key, 'settings.appendOnly', true))" @change="setComponentValue(component.key, 'settings.appendOnly', Boolean($event))" /></a-form-item></a-grid-item><a-grid-item v-if="component.key === 'redis'"><a-form-item label="Cluster超时(ms)"><a-input-number :model-value="Number(componentValue(component.key, 'settings.clusterNodeTimeoutMs', 5000))" :min="1000" :max="60000" @change="setComponentValue(component.key, 'settings.clusterNodeTimeoutMs', Number($event))" /></a-form-item></a-grid-item></template>
		              </a-grid></a-form>
		              <a-alert v-if="component.key === 'activemq' && componentMode(component) === 'cluster'" type="warning" show-icon class="full-card">ActiveMQ Classic 集群模式部署多个 Broker 并通过 Service 分发连接，不等同于共享存储主备；需要业务客户端启用 failover，并按消息可靠性要求进一步配置网络连接器或共享存储。</a-alert>
		            </template>
		            <template v-if="managedDatabaseConsoleKeys.includes(component.key)">
		              <a-divider orientation="left">{{ component.key === 'etcd_workbench' ? 'etcd 管理工具参数' : '数据库管理工具参数' }}</a-divider>
		              <a-alert type="success" show-icon class="full-card">{{ managedConsoleDescription(component.key) }} 对外配置域名时平台强制要求 HTTPS 和 TLS 证书。</a-alert>
		              <a-form :model="catalogConfig(component.key).values" layout="vertical"><a-grid :cols="4" :col-gap="14">
		                <a-grid-item><a-form-item :label="component.key === 'bytebase' ? '管理员邮箱' : 'Web 登录用户名'"><a-input :model-value="databaseConsoleUsername(component.key)" @change="setDatabaseConsoleUsername(component.key, String($event))" /></a-form-item></a-grid-item>
		                <a-grid-item><a-form-item label="StorageClass"><a-input :model-value="String(componentValue(component.key, 'persistence.storageClass', 'gp3'))" @change="setComponentValue(component.key, 'persistence.storageClass', String($event).trim())" /></a-form-item></a-grid-item>
		                <a-grid-item><a-form-item label="磁盘容量"><a-input :model-value="String(componentValue(component.key, 'persistence.size', component.key === 'bytebase' ? '20Gi' : '5Gi'))" @change="setComponentValue(component.key, 'persistence.size', String($event).trim())" /></a-form-item></a-grid-item>
		                <a-grid-item><a-form-item label="卸载时保留 PVC"><a-switch :model-value="Boolean(componentValue(component.key, 'persistence.retainOnDelete', true))" @change="setComponentValue(component.key, 'persistence.retainOnDelete', Boolean($event))" /></a-form-item></a-grid-item>
		                <template v-if="component.key === 'etcd_workbench'">
		                  <a-grid-item :span="2"><a-form-item label="本环境 etcd 地址"><a-input :model-value="etcdWorkbenchEndpoint" disabled /></a-form-item></a-grid-item>
		                  <a-grid-item><a-form-item label="连接超时（毫秒）"><a-input-number :model-value="Number(componentValue(component.key, 'settings.etcdExecuteTimeoutMillis', 5000))" :min="1000" :max="60000" style="width:100%" @change="setComponentValue(component.key, 'settings.etcdExecuteTimeoutMillis', Number($event))" /></a-form-item></a-grid-item>
		                  <a-grid-item><a-form-item label="连接心跳"><a-switch :model-value="Boolean(componentValue(component.key, 'settings.enableHeartbeat', true))" @change="setComponentValue(component.key, 'settings.enableHeartbeat', Boolean($event))" /></a-form-item></a-grid-item>
		                </template>
		              </a-grid></a-form>
		            </template>
		            <template v-if="component.key === 'rabbitmq'">
		              <a-alert type="success" show-icon class="full-card">RabbitMQ Management WebUI 已内置；域名转发的后端选择 {{ catalogConfig(component.key).namespace }}/rabbitmq:15672（HTTP），对外入口必须使用 HTTPS/TLS。资源与访问页会同时展示 AMQP 和 WebUI 入口及管理密码。</a-alert>
		              <a-form :model="catalogConfig(component.key).values" layout="vertical"><a-grid :cols="4" :col-gap="14">
		                <a-grid-item><a-form-item label="WebUI / AMQP 用户名"><a-input :model-value="String(componentValue(component.key, 'auth.username', 'user'))" @change="setRabbitMQUsername(String($event))" /></a-form-item></a-grid-item>
		                <a-grid-item><a-form-item label="StorageClass"><a-input :model-value="String(componentValue(component.key, 'persistence.storageClass', 'gp3'))" @change="setComponentValue(component.key, 'persistence.storageClass', String($event).trim())" /></a-form-item></a-grid-item>
		                <a-grid-item><a-form-item label="磁盘容量"><a-input :model-value="String(componentValue(component.key, 'persistence.size', '8Gi'))" @change="setComponentValue(component.key, 'persistence.size', String($event).trim())" /></a-form-item></a-grid-item>
		                <a-grid-item><a-form-item label="卸载时保留 PVC"><a-switch :model-value="Boolean(componentValue(component.key, 'persistence.retainOnDelete', true))" @change="setComponentValue(component.key, 'persistence.retainOnDelete', Boolean($event))" /></a-form-item></a-grid-item>
		              </a-grid></a-form>
		            </template>
		            <template v-if="component.key === 'loki'">
		              <a-divider orientation="left">Loki 日志存储 · EBS 持久卷</a-divider>
		              <a-alert type="success" show-icon class="full-card">平台会自动部署 Grafana Alloy，采集 EKS 全部 Namespace 的 Pod 日志与 Kubernetes 事件并写入 Loki；同时自动启用 Grafana、创建 Loki 数据源。部署完成后直接访问 Grafana，在 Explore 中选择 Loki 即可查询。日志数据写入 EBS PVC，Pod 重启后仍然保留。</a-alert>
		              <a-form :model="catalogConfig(component.key).values" layout="vertical"><a-grid :cols="5" :col-gap="14">
		                <a-grid-item><a-form-item label="启用持久化"><a-switch :model-value="Boolean(componentValue(component.key, 'singleBinary.persistence.enabled', true))" @change="setComponentValue(component.key, 'singleBinary.persistence.enabled', Boolean($event))" /></a-form-item></a-grid-item>
		                <a-grid-item><a-form-item label="StorageClass"><a-input :model-value="String(componentValue(component.key, 'singleBinary.persistence.storageClass', 'gp3'))" @change="setComponentValue(component.key, 'singleBinary.persistence.storageClass', String($event).trim())" /></a-form-item></a-grid-item>
		                <a-grid-item><a-form-item label="单副本磁盘（GiB）"><a-input-number :model-value="lokiDiskGiB(component.key)" :min="1" :max="16384" @change="setLokiDiskGiB(component.key, Number($event))" /></a-form-item></a-grid-item>
		                <a-grid-item><a-form-item label="日志保留（天）"><a-input-number :model-value="lokiRetentionDays(component.key)" :min="1" :max="3650" @change="setLokiRetentionDays(component.key, Number($event))" /></a-form-item></a-grid-item>
		                <a-grid-item><a-form-item label="卸载时删除PVC" extra="关闭后需要手工清理遗留 EBS 磁盘。"><a-switch :model-value="Boolean(componentValue(component.key, 'singleBinary.persistence.enableStatefulSetAutoDeletePVC', true))" @change="setComponentValue(component.key, 'singleBinary.persistence.enableStatefulSetAutoDeletePVC', Boolean($event))" /></a-form-item></a-grid-item>
		              </a-grid></a-form>
		              <a-alert v-if="componentMode(component) === 'cluster' && componentValue(component.key, 'loki.storage.type', 'filesystem') === 'filesystem'" type="warning" show-icon class="full-card">Loki 多副本生产集群建议改用 S3 对象存储；EBS 文件系统模式更适合单机或小规模测试环境。</a-alert>
		            </template>
		            <div v-if="componentAdvancedEnabled(component, 'helm_values')" class="component-parameter-actions"><div><strong>环境级 Helm Values</strong><small>当前 {{ componentValuesSummary(component.key) }}；只添加需要覆盖的参数，不写入整套默认值。</small></div><a-button type="outline" @click="openComponentValues(component)">选择高级参数</a-button></div>
	          </a-collapse-item></a-collapse>
	        </a-card>
      </a-tab-pane>

	      <a-tab-pane key="tls">
          <template #title><span class="deployment-stage-tab-title"><span class="deployment-stage-badge phase-two">2</span><span>TLS 证书</span></span></template>
		        <a-alert type="info" show-icon class="full-card">先建立 TLS 证书配置，再由域名规则引用。保存 TLS 配置后平台会自动创建部署任务：直接粘贴的证书只创建或更新 Kubernetes TLS Secret，不会重装阶段2组件；cert-manager 自动签发首次需要同时安装 cert-manager，因此会自动执行阶段2。无需生成计划或再次点击执行。证书正文与私钥会加密保存且永不回显。</a-alert>
	        <a-card><template #title><span class="card-title">TLS 证书配置</span></template><template #extra><a-button type="primary" size="small" @click="openCertificate()"><icon-plus />新增证书配置</a-button></template>
		          <a-table :data="form.tls.certificates" :loading="store.loadingTLSCertificates" :pagination="false" size="small"><template #columns><a-table-column title="标识" data-index="key" /><a-table-column title="模式"><template #cell="{ record }"><a-tag :color="certificateModeColor(record.mode)">{{ certificateModeName(record.mode) }}</a-tag></template></a-table-column><a-table-column title="证书域名"><template #cell="{ record }">{{ certificateDomains(record) }}</template></a-table-column><a-table-column title="TLS Secret" data-index="tls_secret_name" /><a-table-column title="Namespace" data-index="namespace" /><a-table-column title="状态"><template #cell="{ record }"><a-tag v-if="record.mode === 'uploaded-pem'" :color="tlsMaterialInfo(record.key) ? 'green' : 'red'">{{ tlsMaterialInfo(record.key) ? '证书已加密保存' : '缺少证书材料' }}</a-tag><a-tag v-else color="gray">部署时对接</a-tag></template></a-table-column><a-table-column title="操作" :width="150"><template #cell="{ rowIndex }"><a-space><a-button size="mini" @click="openCertificate(rowIndex)">编辑</a-button><a-popconfirm content="删除该证书配置？引用它的域名规则需要同步调整。" @ok="removeCertificate(rowIndex)"><a-button size="mini" status="danger">删除</a-button></a-popconfirm></a-space></template></a-table-column></template><template #empty><a-empty description="尚未配置 TLS 证书" /></template></a-table>
	        </a-card>
	      </a-tab-pane>

	      <a-tab-pane key="domains">
          <template #title><span class="deployment-stage-tab-title"><span class="deployment-stage-badge phase-two">2</span><span>域名转发</span></span></template>
	        <a-alert type="info" show-icon class="full-card">HTTP/HTTPS、WebSocket、gRPC/gRPCS 通过 Higress 或 NGINX Ingress 按域名、路径和 Service 端口转发；MySQL、Redis、Kafka 等原生 TCP 服务使用独立 AWS NLB。公网 TCP 必须设置来源 CIDR 白名单，且禁止全网开放。</a-alert>
	        <a-card><template #title><span class="card-title">域名与转发规则</span></template><template #extra><a-space>
	            <a-tooltip :content="domainSyncHint"><a-button size="small" :loading="syncingDomains" :disabled="!canSyncDomains" @click="previewDomainsFromIngress"><icon-refresh />同步 EKS Ingress</a-button></a-tooltip>
	            <a-button type="primary" size="small" @click="openDomain()"><icon-plus />新增域名</a-button>
	          </a-space></template>
	          <a-table :data="form.domains" :pagination="false" size="small">
	            <template #columns>
	              <a-table-column title="协议" :width="110"><template #cell="{ record }"><a-tag :color="routeProtocol(record) === 'tcp' ? 'orange' : routeIsSecure(record) ? 'green' : 'blue'">{{ routeProtocolLabel(record) }}</a-tag></template></a-table-column>
	              <a-table-column title="访问入口"><template #cell="{ record }">{{ record.access_type === 'ip' ? (routeProtocol(record) === 'tcp' ? 'AWS NLB 地址' : '网关公网 IP / LB 地址') : record.domain }}</template></a-table-column>
	              <a-table-column title="转发方式" :width="120"><template #cell="{ record }"><a-tag>{{ routeProtocol(record) === 'tcp' ? (record.tcp_scheme === 'internal' ? '内网 NLB' : '公网 NLB') : record.gateway }}</a-tag></template></a-table-column>
	              <a-table-column title="路由与后端"><template #cell="{ record }"><div class="domain-route-summary"><div v-for="route in domainRoutes(record)" :key="`${route.path}-${route.service}-${route.service_port}`"><code>{{ routeProtocol(record) === 'tcp' ? `:${record.external_port || record.service_port}` : (route.path || '/') }}</code><span>{{ record.namespace }}/{{ route.service }}:{{ route.service_port }}</span></div></div></template></a-table-column>
	              <a-table-column title="安全" :width="150"><template #cell="{ record }"><a-tag :color="routeProtocol(record) === 'tcp' ? 'orange' : record.tls_enabled ? 'green' : 'gray'">{{ routeProtocol(record) === 'tcp' ? `${record.allowed_cidrs?.length || 0} 条白名单` : record.tls_enabled ? (record.certificate_ref || '未绑定证书') : '无 TLS' }}</a-tag></template></a-table-column>
	              <a-table-column title="操作" :width="140"><template #cell="{ rowIndex }"><a-space><a-button size="mini" @click="openDomain(rowIndex)">编辑</a-button><a-popconfirm content="删除该域名及其全部转发路由？" @ok="form.domains.splice(rowIndex, 1)"><a-button size="mini" status="danger">删除</a-button></a-popconfirm></a-space></template></a-table-column>
	            </template>
	            <template #empty><a-empty description="尚未配置转发规则" /></template>
	          </a-table>
        </a-card>
      </a-tab-pane>

	      <a-tab-pane key="alerts">
          <template #title><span class="deployment-stage-tab-title"><span class="deployment-stage-badge phase-two">2</span><span>告警管理</span></span></template>
	        <a-card class="full-card">
	          <template #title><span class="card-title">告警中心</span></template>
	          <template #extra><a-space><a-popconfirm content="将向当前环境全部已保存告警通道依次发送全部场景测试卡片，确认继续？" @ok="testAllAlertScenarios"><a-button size="small" type="primary" :loading="testingAlertScenario === 'all'" :disabled="!canTestAlertScenarios">测试告警功能</a-button></a-popconfirm><span class="alert-center-switch-label">启用自动告警</span><a-switch v-model="form.alerting.enabled" /></a-space></template>
	          <a-alert type="info" show-icon>“核心告警”只推送节点与容量、工作负载、服务可用性、数据库、部署任务和监控链路故障；目标发现抖动、录制规则和纯提示类事件仍保留在 Prometheus/Grafana，但不发送通知。</a-alert>
	          <div class="alert-delivery-policy">
	            <div><strong>通知策略</strong><small>推荐使用核心模式，避免无效告警干扰排障。</small></div>
	            <a-radio-group v-model="form.alerting.delivery_policy" type="button"><a-radio value="core">核心告警（推荐）</a-radio><a-radio value="all">全部告警</a-radio></a-radio-group>
	          </div>
	          <a-alert v-if="!form.alerting.enabled && form.alerting.channels?.length" type="warning" show-icon style="margin-top:12px">当前告警中心已关闭，测试按钮仍可验证 Webhook，但自动监控告警不会生效。请开启后保存配置并执行阶段 2。</a-alert>
	        </a-card>
	        <a-card class="full-card alert-scenario-card">
	          <template #title><span class="card-title">告警场景测试</span></template>
	          <template #extra>
	            <a-popconfirm content="将依次向全部已保存通道发送全部告警场景测试消息，确认继续？" @ok="testAllAlertScenarios">
	              <a-button size="small" type="primary" :loading="testingAlertScenario === 'all'" :disabled="!canTestAlertScenarios">测试全部类型</a-button>
	            </a-popconfirm>
	          </template>
	          <div class="alert-scenario-grid">
	            <button v-for="scenario in alertScenarios" :key="scenario.key" type="button" class="alert-scenario-item" :class="`severity-${scenario.severity}`" :disabled="!canTestAlertScenarios || Boolean(testingAlertScenario)" @click="testAlertScenario(scenario.key)">
	              <span class="alert-scenario-symbol">{{ scenario.symbol }}</span>
	              <span class="alert-scenario-copy"><strong>{{ scenario.name }}</strong><small>{{ scenario.description }}</small></span>
	              <span class="alert-scenario-action">{{ testingAlertScenario === scenario.key ? '发送中…' : '发送测试' }}</span>
	            </button>
	          </div>
	          <small class="alert-scenario-help">场景测试不受“告警中心”开关影响，用于验证当前环境已保存的模板渲染、Webhook 连通性和通道送达；不会在集群中制造真实故障。</small>
	        </a-card>
	        <div class="content-grid"><a-card><template #title><span class="card-title">告警通道</span></template><template #extra><a-button size="small" type="primary" @click="channelVisible = true"><icon-plus />新增</a-button></template><a-table :data="form.alerting.channels" :pagination="false" size="small"><template #columns><a-table-column title="名称" data-index="name" /><a-table-column title="类型" data-index="type" /><a-table-column title="接收地址"><template #cell="{ record }"><a-tooltip :content="record.address"><span class="ellipsis-text">{{ record.address }}</span></a-tooltip></template></a-table-column><a-table-column title="认证引用"><template #cell="{ record }">{{ record.secret_ref || '无' }}</template></a-table-column><a-table-column title="操作" :width="132"><template #cell="{ record, rowIndex }"><a-space><a-button size="mini" :loading="testingAlertChannel === record.name" :disabled="!store.canConfigure" @click="testAlertChannel(record)">测试</a-button><a-button size="mini" status="danger" @click="form.alerting.channels.splice(rowIndex,1)">删除</a-button></a-space></template></a-table-column></template></a-table></a-card>
	          <a-card><template #title><span class="card-title">Markdown 告警模板</span></template><template #extra><a-space><a-popconfirm content="将更新平台预置模板，自己新增的模板会保留，确认继续？" @ok="restoreDefaultAlertTemplates"><a-button size="small">应用新版预置模板</a-button></a-popconfirm><a-button size="small" type="primary" @click="templateVisible = true"><icon-plus />新增</a-button></a-space></template><a-table :data="form.alerting.templates" :pagination="false" size="small"><template #columns><a-table-column title="名称" data-index="name" /><a-table-column title="告警类型" data-index="event_type" /><a-table-column title="级别" data-index="severity" /><a-table-column title="格式" data-index="format"><template #cell="{ record }"><a-tag color="arcoblue">{{ record.format || 'markdown' }}</a-tag></template></a-table-column><a-table-column title="标题" data-index="title" /><a-table-column title="操作"><template #cell="{ rowIndex }"><a-button size="mini" status="danger" @click="form.alerting.templates.splice(rowIndex,1)">删除</a-button></template></a-table-column></template></a-table></a-card></div>
      </a-tab-pane>
    </a-tabs>

	    <div class="form-actions">
	          <div><strong>{{ stageReadiness }}</strong><small>{{ enabledSummary }}</small></div>
          <a-space>
	            <a-dropdown :disabled="!store.canConfigure && !store.canDeploy"><a-button status="danger">危险操作<icon-down /></a-button><template #content><a-doption :disabled="!store.canConfigure" @click="openDeleteEnvironment">删除项目环境</a-doption><a-doption :disabled="!store.canDeploy || !awsCredentialReady" @click="destroyVisible = true">销毁 AWS 资源</a-doption></template></a-dropdown>
	            <a-button :loading="saving || (activeTab === 'tls' && jobSubmitting)" :disabled="!dirty || !store.canConfigure || environmentBusy" @click="save"><icon-save />{{ activeTab === 'tls' ? '保存并应用 TLS' : '保存配置' }}</a-button>
	            <a-button v-if="activeTab !== 'tls'" type="primary" size="large" :loading="jobSubmitting || saving" :disabled="deploymentAction.disabled" :title="deploymentAction.reason" @click="startCurrentPhase"><icon-play-arrow />{{ deploymentButtonLabel }}</a-button>
          </a-space>
        </div>
  </div>
  <a-empty v-else description="请先创建并选择项目环境" />

  <a-modal v-model:visible="nodeVisible" title="添加节点组" @before-ok="addNodeGroup"><a-alert v-if="nodePlanningLocked" type="warning" show-icon class="full-card">这是增量创建：保存后只能继续调整 Min / Max，不能删除或修改该节点组的规格、网络与用途。</a-alert><a-form :model="{}" layout="vertical"><a-form-item label="节点组名称"><a-input v-model="newNodeGroup" placeholder="workers" /></a-form-item></a-form></a-modal>
	<a-modal v-model:visible="instanceCatalogVisible" :title="`AWS EC2 实例规格 · ${instanceCatalogGroup || '节点组'}`" width="1120px" :footer="false">
	  <a-alert type="info" show-icon class="full-card">规格从 {{ form.region }} 的 AWS EC2 API 实时读取，只返回该 Region 支持的实例类型。查询需要 <code>ec2:DescribeInstanceTypes</code> 只读权限。</a-alert>
	  <div class="instance-catalog-toolbar"><a-input-search v-model="instanceCatalogQuery" search-button :loading="loadingInstanceTypes" placeholder="输入实例族或规格，例如 m7i、c7g、r7i.2xlarge" @search="loadInstanceTypes" /><a href="https://aws.amazon.com/ec2/instance-types/" target="_blank" rel="noopener noreferrer" class="aws-official-link">查看 AWS 官方实例类型说明</a></div>
	  <a-alert v-if="instanceCatalogError" type="warning" show-icon class="full-card">{{ instanceCatalogError }}</a-alert>
	  <a-table :data="instanceTypes" :pagination="false" row-key="name" :scroll="{ y: 430 }" size="small">
	    <template #columns><a-table-column title="实例类型" :width="150"><template #cell="{ record }"><strong>{{ record.name }}</strong><a-tag v-if="record.current_generation" size="small" color="green">当前代</a-tag></template></a-table-column><a-table-column title="vCPU" data-index="vcpu" :width="75" /><a-table-column title="内存" :width="95"><template #cell="{ record }">{{ formatMemory(record.memory_mib) }}</template></a-table-column><a-table-column title="架构" :width="120"><template #cell="{ record }">{{ (record.architectures || []).join(' / ') }}</template></a-table-column><a-table-column title="网络性能" data-index="network_performance" /><a-table-column title="最大 ENI" data-index="maximum_network_interfaces" :width="85" /><a-table-column title="购买方式" :width="145"><template #cell="{ record }">{{ (record.usage_classes || []).join(' / ') }}</template></a-table-column><a-table-column title="节点组候选" :width="115" fixed="right"><template #cell="{ record }"><a-button size="small" :type="instanceSelected(record.name) ? 'outline' : 'primary'" :disabled="nodeGroupFieldLocked(instanceCatalogGroup)" @click="toggleInstanceType(record.name)">{{ nodeGroupFieldLocked(instanceCatalogGroup) ? '只读' : (instanceSelected(record.name) ? '移除' : '加入') }}</a-button></template></a-table-column></template>
	    <template #empty><a-empty :description="loadingInstanceTypes ? '正在查询 AWS 实例目录' : '输入实例族后查询，例如 m7i'" /></template>
	  </a-table>
		</a-modal>
	  <a-modal v-model:visible="componentValuesVisible" :title="`${componentValuesName} · 高级 Helm 参数`" width="1040px" ok-text="应用参数" @before-ok="saveComponentValues">
	    <a-alert type="warning" show-icon class="full-card">只保留当前环境确实需要覆盖的参数。不要填写明文密码、Token、Access Key 或私钥，请使用 Kubernetes Secret / AWS Secrets Manager 引用。</a-alert>
	    <div class="component-values-toolbar"><a-space><a-button :loading="componentValuesLoading" :disabled="!componentValuesCanInspect" @click="loadComponentChartDefaults"><icon-download />查询 Chart 可选参数</a-button><a-button @click="resetComponentValues">恢复打开时配置</a-button></a-space><span v-if="componentValuesMessage" :class="{ 'danger-text': componentValuesError }">{{ componentValuesMessage }}</span></div>
	    <div v-if="componentDefaultParameterRows.length" class="helm-parameter-picker"><div><strong>添加参数</strong><small>从 Chart 默认 Values 中选择；未选择的参数不会写入部署配置。</small></div><a-select v-model="componentValuesCandidatePaths" multiple allow-search allow-clear :max-tag-count="3" placeholder="搜索并选择参数路径"><a-option v-for="record in componentDefaultParameterRows" :key="record.path" :value="record.path">{{ record.path }} · {{ parameterPreview(record.value) }}</a-option></a-select><a-button type="primary" :disabled="!componentValuesCandidatePaths.length" @click="addSelectedComponentParameters"><icon-plus />添加所选</a-button></div>
	    <a-tabs v-model:active-key="componentValuesTab" type="rounded">
	      <a-tab-pane key="form" :title="`已选参数（${componentParameterRows.length}）`">
	        <a-table :data="componentParameterRows" :pagination="{ pageSize: 20, showTotal: true }" row-key="path" size="small" :scroll="{ y: 420 }">
	          <template #columns><a-table-column title="参数路径" data-index="path" :width="350"><template #cell="{ record }"><code>{{ record.path }}</code></template></a-table-column><a-table-column title="类型" data-index="type" :width="80" /><a-table-column title="覆盖值"><template #cell="{ record }"><a-switch v-if="record.type === 'boolean'" :model-value="record.value" @update:model-value="updateComponentParameter(record.path, Boolean($event))" /><a-input-number v-else-if="record.type === 'number'" :model-value="record.value" @update:model-value="updateComponentParameter(record.path, Number($event))" /><a-textarea v-else-if="record.type === 'json'" :model-value="JSON.stringify(record.value)" :auto-size="{ minRows: 1, maxRows: 5 }" @change="updateComponentJSONParameter(record.path, String($event))" /><a-input v-else :model-value="String(record.value ?? '')" @update:model-value="updateComponentParameter(record.path, String($event))" /></template></a-table-column><a-table-column title="操作" :width="74" align="center"><template #cell="{ record }"><a-popconfirm content="删除这个环境级覆盖参数？" @ok="removeComponentParameter(record.path)"><a-button size="mini" status="danger"><icon-delete /></a-button></a-popconfirm></template></a-table-column></template>
	          <template #empty><a-empty description="尚未选择高级参数；可以从上方查询 Chart 参数，或切换到 YAML 手工添加" /></template>
	        </a-table>
	      </a-tab-pane>
	      <a-tab-pane key="yaml" title="YAML"><a-textarea v-model="componentValuesYAML" class="environment-yaml-editor" :auto-size="{ minRows: 20, maxRows: 30 }" placeholder="replicaCount: 1" /><small class="field-help">最大 1 MiB。应用后仍需点击页面底部“保存配置”，阶段 2 才会使用新参数。</small></a-tab-pane>
	    </a-tabs>
	  </a-modal>
	  <a-modal
	    v-model:visible="componentRemovalVisible"
	    :title="`卸载 ${pendingComponentRemoval?.display_name || '组件'}`"
	    width="680px"
	    ok-text="确认标记卸载"
	    @before-ok="confirmComponentRemoval"
	    @cancel="resetComponentRemoval"
	  >
	    <a-alert type="warning" show-icon class="full-card">本次只会把部署配置改为“待卸载”，不会立即操作 EKS。保存配置并执行对应阶段后才会实际卸载。</a-alert>
	    <a-descriptions v-if="pendingComponentRemoval" :column="2" bordered size="small">
	      <a-descriptions-item label="组件">{{ pendingComponentRemoval.display_name }}</a-descriptions-item>
	      <a-descriptions-item label="生效阶段">{{ componentRemovalPhaseLabel }}</a-descriptions-item>
	      <a-descriptions-item label="当前集群状态">{{ componentRemovalActualLabel }}</a-descriptions-item>
	      <a-descriptions-item label="Namespace / Release">{{ componentRemovalScope }}</a-descriptions-item>
	      <a-descriptions-item label="Namespace 策略" :span="2">只卸载该组件自己的 Release 与关联资源，Namespace 永久保留。</a-descriptions-item>
	      <a-descriptions-item label="数据卷策略" :span="2">{{ componentRemovalRetentionText }}</a-descriptions-item>
	      <a-descriptions-item label="关联资源" :span="2">{{ componentRemovalRelatedResources }}</a-descriptions-item>
	    </a-descriptions>
	    <a-alert v-if="componentRemovalWarning" type="warning" show-icon class="full-card" style="margin-top:12px">{{ componentRemovalWarning }}</a-alert>
	    <a-checkbox v-model="componentRemovalAcknowledged" style="margin-top:16px">我已确认访问中断和数据保留策略，并知道需要保存后执行对应阶段。</a-checkbox>
	  </a-modal>
	  <a-modal
	    v-model:visible="cloudServiceRemovalVisible"
	    :title="`删除 ${pendingCloudServiceRemoval?.title || 'AWS 云服务'}`"
	    width="660px"
	    ok-text="确认标记删除"
	    @before-ok="confirmCloudServiceRemoval"
	    @cancel="resetCloudServiceRemoval"
	  >
	    <a-alert :type="pendingCloudServiceRemovalKey === 'ecr' ? 'info' : 'error'" show-icon class="full-card">{{ cloudServiceRemovalSummary }}</a-alert>
	    <a-descriptions v-if="pendingCloudServiceRemoval" :column="1" bordered size="small">
	      <a-descriptions-item label="生效时机">保存配置后，执行“更新部署【阶段一】”</a-descriptions-item>
	      <a-descriptions-item label="数据处理">{{ cloudServiceRemovalDataPolicy }}</a-descriptions-item>
	      <a-descriptions-item label="保护检查">{{ cloudServiceRemovalProtection }}</a-descriptions-item>
	    </a-descriptions>
	    <a-checkbox v-model="cloudServiceRemovalAcknowledged" style="margin-top:16px">我已确认数据删除、备份/快照和访问中断影响。</a-checkbox>
	  </a-modal>
	  <a-modal v-model:visible="managedStorageResizeVisible" :title="`${managedStorageResizeOperation === 'expand' ? '扩容' : '安全缩容'} ${managedStorageName(managedStorageResizeTarget?.component || '')}`" :ok-text="managedStorageResizeOperation === 'expand' ? '开始在线扩容' : '开始迁移缩容'" :ok-loading="submittingManagedStorageResize" @before-ok="submitManagedStorageResize" @cancel="resetManagedStorageResize">
	    <a-alert :type="managedStorageResizeOperation === 'expand' ? 'info' : 'warning'" show-icon class="full-card">
	      {{ managedStorageResizeOperation === 'expand'
	        ? '平台将在线增大 PVC，不停止工作负载。AWS EBS 和文件系统完成扩容可能需要几分钟。'
	        : 'Kubernetes 和 EBS 不支持原地缩小磁盘。平台会停止该子组件，把数据复制到新 PVC，切换后验证；验证失败自动切回原盘，旧 PVC 不会删除。' }}
	    </a-alert>
	    <a-descriptions v-if="managedStorageResizeTarget" :column="2" size="small" bordered>
	      <a-descriptions-item label="当前 PVC">{{ managedStorageResizeTarget.namespace }}/{{ managedStorageResizeTarget.pvc_name }}</a-descriptions-item>
	      <a-descriptions-item label="当前容量">{{ managedStorageResizeTarget.requested || managedStorageResizeTarget.capacity }}</a-descriptions-item>
	      <a-descriptions-item label="StorageClass">{{ managedStorageResizeTarget.storage_class }}</a-descriptions-item>
	      <a-descriptions-item label="操作范围">{{ managedStorageName(managedStorageResizeTarget.component) }} 的全部活动 PVC</a-descriptions-item>
	    </a-descriptions>
	    <a-form :model="{}" layout="vertical" style="margin-top:16px">
	      <a-form-item label="目标容量（GiB）" required><a-input-number v-model="managedStorageTargetGi" :min="1" :max="16384" style="width:100%" /></a-form-item>
	      <a-form-item v-if="managedStorageResizeOperation === 'shrink'" label="数据安全余量"><a-input-number v-model="managedStorageSafetyPercent" :min="10" :max="100" :formatter="(value: string) => `${value}%`" :parser="(value: string) => value.replace('%','')" style="width:100%" /></a-form-item>
	      <a-form-item v-if="managedStorageResizeOperation === 'shrink'"><a-checkbox v-model="managedStorageResizeAcknowledged">我确认缩容期间该子组件会短暂停服，且会保留原 PVC 用于回滚。</a-checkbox></a-form-item>
	    </a-form>
	  </a-modal>
		  <a-modal v-model:visible="certificateVisible" title="TLS 证书配置" width="900px" :ok-loading="certificateSaving" @before-ok="saveCertificate" @cancel="clearCertificateDraft">
		    <a-form :model="certificateDraft" layout="vertical">
		      <a-grid :cols="2" :col-gap="14">
		        <a-grid-item><a-form-item label="证书标识" required extra="保存后不可修改，用于域名规则引用。"><a-input v-model="certificateDraft.key" :disabled="certificateIndex >= 0" placeholder="web-tls" /></a-form-item></a-grid-item>
		        <a-grid-item><a-form-item label="对接模式"><a-select v-model="certificateDraft.mode"><a-option value="cert-manager">cert-manager自动签发</a-option><a-option value="uploaded-pem">直接粘贴证书并生成Secret</a-option><a-option value="existing-secret">引用已有Kubernetes TLS Secret</a-option></a-select></a-form-item></a-grid-item>
		        <a-grid-item><a-form-item label="Namespace"><a-select v-model="certificateDraft.namespace"><a-option v-for="row in componentNamespaceRows" :key="row.name" :value="row.name">{{ row.name }}</a-option></a-select></a-form-item></a-grid-item>
		        <a-grid-item><a-form-item label="TLS Secret名称" required><a-input v-model="certificateDraft.tls_secret_name" placeholder="web-tls" /></a-form-item></a-grid-item>
		        <a-grid-item v-if="certificateDraft.mode === 'cert-manager'" :span="2"><a-form-item label="证书域名" required><a-input-tag v-model="certificateDraft.domains" placeholder="app.example.com" /></a-form-item></a-grid-item>
		        <a-grid-item v-if="certificateDraft.mode === 'cert-manager'"><a-form-item label="Issuer名称"><a-input v-model="certificateDraft.issuer_name" /></a-form-item></a-grid-item>
		        <a-grid-item v-if="certificateDraft.mode === 'cert-manager'"><a-form-item label="Issuer类型"><a-select v-model="certificateDraft.issuer_kind"><a-option value="ClusterIssuer">ClusterIssuer</a-option><a-option value="Issuer">Issuer</a-option></a-select></a-form-item></a-grid-item>
		        <a-grid-item v-if="certificateDraft.mode === 'uploaded-pem'" :span="2"><a-alert :type="currentTLSMaterial ? 'success' : 'warning'" show-icon class="full-card">{{ currentTLSMaterial ? `证书材料已加密保存，有效期至 ${formatCertificateTime(currentTLSMaterial.not_after)}。留空可继续使用；重新粘贴将安全覆盖。` : '请同时粘贴完整证书链和未加密私钥。平台会校验证书有效期及密钥匹配关系。' }}</a-alert></a-grid-item>
		        <a-grid-item v-if="certificateDraft.mode === 'uploaded-pem'" :span="2"><a-form-item label="证书链（PEM）" :required="!currentTLSMaterial" extra="从服务器证书开始，可继续拼接中间证书。"><a-textarea v-model="certificateDraft.certificate_pem" :auto-size="{ minRows: 7, maxRows: 14 }" placeholder="-----BEGIN CERTIFICATE-----" autocomplete="off" /></a-form-item></a-grid-item>
		        <a-grid-item v-if="certificateDraft.mode === 'uploaded-pem'" :span="2"><a-form-item label="证书私钥（KEY）" :required="!currentTLSMaterial" extra="粘贴与上方证书匹配的 KEY；支持 PKCS#8、PKCS#1 和 EC 未加密私钥，保存后不会回显。"><a-textarea v-model="certificateDraft.private_key_pem" :auto-size="{ minRows: 7, maxRows: 14 }" placeholder="-----BEGIN PRIVATE KEY-----" autocomplete="off" /></a-form-item></a-grid-item>
		      </a-grid>
		    </a-form>
		  </a-modal>
	  <a-modal v-model:visible="domainVisible" title="访问与转发配置" width="1080px" @before-ok="saveDomain">
	    <a-form :model="domainDraft" layout="vertical">
	      <a-grid :cols="2" :col-gap="14">
	        <a-grid-item><a-form-item label="业务协议" required><a-select v-model="domainDraft.protocol" @change="changeDomainProtocol"><a-option value="http">HTTP</a-option><a-option value="https">HTTPS</a-option><a-option value="ws">WebSocket (WS)</a-option><a-option value="wss">WebSocket Secure (WSS)</a-option><a-option value="grpc">gRPC</a-option><a-option value="grpcs">gRPC Secure (gRPCS)</a-option><a-option value="tcp">TCP</a-option></a-select></a-form-item></a-grid-item>
	        <a-grid-item><a-form-item label="访问方式"><a-radio-group v-model="domainDraft.access_type" type="button" @change="changeDomainAccessType"><a-radio value="domain">域名</a-radio><a-radio value="ip">负载均衡地址</a-radio></a-radio-group></a-form-item></a-grid-item>
	        <a-grid-item v-if="domainDraft.access_type !== 'ip'"><a-form-item label="域名" required><a-input v-model="domainDraft.domain" placeholder="app.example.com" /></a-form-item></a-grid-item>
	        <a-grid-item v-if="!isTCPRoute"><a-form-item label="网关"><a-select v-model="domainDraft.gateway"><a-option value="higress">Higress</a-option><a-option value="nginx">NGINX Ingress</a-option></a-select></a-form-item></a-grid-item>
	        <a-grid-item v-else><a-form-item label="NLB 类型"><a-radio-group v-model="domainDraft.tcp_scheme" type="button"><a-radio value="internet-facing">公网</a-radio><a-radio value="internal">内网</a-radio></a-radio-group></a-form-item></a-grid-item>
	        <a-grid-item><a-form-item label="Namespace" required><a-select v-model="domainDraft.namespace" :loading="loadingKubernetesServices" allow-search placeholder="从 EKS 集群选择 Namespace" @change="changeDomainNamespace"><a-option v-if="domainNamespaceMissing" :value="domainDraft.namespace">{{ domainDraft.namespace }} · 当前配置（集群未发现）</a-option><a-option v-for="namespace in kubernetesServiceNamespaces" :key="namespace" :value="namespace">{{ namespace }}</a-option></a-select></a-form-item></a-grid-item>
	        <a-grid-item v-if="isTCPRoute"><a-form-item label="Service 端口" required><a-select v-model="domainDraft.service_port" :disabled="!domainDraft.service" placeholder="选择 Service 暴露端口" @change="changeDomainServicePort"><a-option v-if="domainPortMissing" :value="domainDraft.service_port">{{ domainDraft.service_port }} · 当前配置（Service 未发现）</a-option><a-option v-for="port in domainServicePorts" :key="`${port.name}-${port.port}`" :value="port.port">{{ port.name ? `${port.name} · ${port.port}` : port.port }}</a-option></a-select></a-form-item></a-grid-item>
	        <a-grid-item v-if="isTCPRoute" :span="2"><a-form-item label="后端 Service" required><div class="eks-version-field domain-service-field"><a-select v-model="domainDraft.service" :loading="loadingKubernetesServices" :disabled="!domainDraft.namespace" :trigger-props="{ autoFitPopupWidth: true }" allow-search placeholder="从 EKS 集群选择 Service" @popup-visible-change="(visible) => visible && loadKubernetesServices()" @change="changeDomainService"><a-option v-if="domainServiceMissing" :value="domainDraft.service">{{ domainDraft.service }} · 当前配置（集群未发现）</a-option><a-option v-for="service in domainServiceOptions" :key="service.name" :value="service.name" :disabled="service.endpoint_health_known && service.type !== 'ExternalName' && service.ready_endpoints === 0">{{ service.name }} · {{ service.type || 'ClusterIP' }} · {{ serviceEndpointLabel(service) }}</a-option></a-select><a-button :loading="loadingKubernetesServices" @click="loadKubernetesServices(true)"><icon-refresh /></a-button></div><small class="field-help" :class="{ 'danger-text': kubernetesServicesError }">{{ kubernetesServicesHint }}</small><a-alert v-if="selectedDomainService && selectedDomainService.endpoint_health_known && selectedDomainService.type !== 'ExternalName' && selectedDomainService.ready_endpoints === 0" type="error" show-icon style="margin-top:10px">该 Service 当前没有 Ready Endpoint，部署后将无法转发流量；请先恢复对应 Pod。</a-alert></a-form-item></a-grid-item>
	        <a-grid-item v-if="!isTCPRoute"><a-form-item label="网关到 Service"><a-select v-if="isGRPCRoute" v-model="domainDraft.backend_protocol"><a-option value="grpc">gRPC / h2c（常用）</a-option><a-option value="grpcs">gRPC over TLS</a-option></a-select><a-select v-else v-model="domainDraft.backend_protocol"><a-option value="http">HTTP / WebSocket（常用）</a-option><a-option value="https">HTTPS / Secure WebSocket</a-option></a-select><small class="field-help">{{ isGRPCRoute ? 'gRPCS 仅表示客户端到网关使用 TLS；除非 Pod 自己终止 TLS，否则这里请选择 gRPC / h2c。' : 'WSS 仅表示客户端到网关使用 TLS；除非 Pod 自己监听 TLS，否则这里请选择 HTTP。' }}</small></a-form-item></a-grid-item>
	        <a-grid-item v-else><a-form-item label="NLB 对外端口" required><a-input-number v-model="domainDraft.external_port" :min="1" :max="65535" style="width:100%" /></a-form-item></a-grid-item>
	        <a-grid-item v-if="routeIsSecure(domainDraft)" :span="2"><a-form-item label="TLS 证书配置" required><a-select v-model="domainDraft.certificate_ref" placeholder="请先创建 TLS 证书配置"><a-option v-for="certificate in form.tls.certificates" :key="certificate.key" :value="certificate.key">{{ certificate.key }} · {{ certificate.tls_secret_name }}</a-option></a-select></a-form-item></a-grid-item>
	        <a-grid-item v-if="isTCPRoute && domainDraft.tcp_scheme === 'internet-facing'" :span="2"><a-form-item label="来源 CIDR 白名单" required><a-textarea v-model="domainDraft.allowed_cidrs_text" :auto-size="{ minRows: 3, maxRows: 6 }" placeholder="每行一个，例如：203.0.113.10/32" /><small class="field-help">支持换行、逗号或空格分隔；为保护数据库和中间件，不允许 0.0.0.0/0 或 ::/0。</small></a-form-item></a-grid-item>
	        <a-grid-item v-if="!isTCPRoute" :span="2">
	          <div class="domain-routes-editor">
	            <div class="domain-routes-header"><div><strong>路径转发规则</strong><small>同一域名可按不同路径转发到当前 Namespace 中的不同 Service。</small></div><a-button type="outline" size="small" @click="addDomainRoute"><icon-plus />添加路由</a-button></div>
	            <a-alert type="info" show-icon>业务协议、TLS、网关及上游协议由当前域名共享；每条路由独立选择路径、匹配方式、Service 和端口。</a-alert>
	            <div v-for="(route, routeIndex) in domainDraft.routes" :key="route._key || routeIndex" class="domain-route-editor-row">
	              <div class="domain-route-editor-title"><strong>路由 {{ Number(routeIndex) + 1 }}</strong><span v-if="domainRouteHealthText(route)" :class="{ 'danger-text': domainRouteHasNoEndpoint(route) }">{{ domainRouteHealthText(route) }}</span><a-button v-if="domainDraft.routes.length > 1" size="mini" status="danger" @click="removeDomainRoute(Number(routeIndex))"><icon-delete />删除</a-button></div>
	              <a-grid :cols="12" :col-gap="12">
	                <a-grid-item :span="3"><a-form-item label="转发路径" required><a-input v-model="route.path" placeholder="/api" /></a-form-item></a-grid-item>
	                <a-grid-item :span="2"><a-form-item label="匹配方式"><a-select v-model="route.path_type"><a-option value="Prefix">Prefix</a-option><a-option value="Exact">Exact</a-option><a-option value="ImplementationSpecific">控制器规则</a-option></a-select></a-form-item></a-grid-item>
	                <a-grid-item :span="5"><a-form-item label="后端 Service" required><a-select v-model="route.service" :disabled="!domainDraft.namespace" allow-search :trigger-props="{ autoFitPopupWidth: true }" placeholder="选择 Service" @popup-visible-change="(visible) => visible && loadKubernetesServices()" @change="changeHTTPRouteService(route, $event)"><a-option v-if="domainRouteServiceMissing(route)" :value="route.service">{{ route.service }} · 当前配置（集群未发现）</a-option><a-option v-for="service in domainServiceOptions" :key="service.name" :value="service.name" :disabled="service.endpoint_health_known && service.type !== 'ExternalName' && service.ready_endpoints === 0">{{ service.name }} · {{ service.type || 'ClusterIP' }} · {{ serviceEndpointLabel(service) }}</a-option></a-select></a-form-item></a-grid-item>
	                <a-grid-item :span="2"><a-form-item label="Service 端口" required><a-select v-model="route.service_port" :disabled="!route.service" placeholder="选择端口"><a-option v-if="domainRoutePortMissing(route)" :value="route.service_port">{{ route.service_port }} · 当前配置</a-option><a-option v-for="port in domainRouteServicePorts(route)" :key="`${port.name}-${port.port}`" :value="port.port">{{ port.name ? `${port.name} · ${port.port}` : port.port }}</a-option></a-select></a-form-item></a-grid-item>
	              </a-grid>
	            </div>
	          </div>
	        </a-grid-item>
	      </a-grid>
	      <a-alert v-if="isTCPRoute" type="warning" show-icon>TCP 不支持域名 Host/路径分流；平台会为本规则创建独立 AWS NLB。自定义域名需要在 DNS 服务商处 CNAME 到部署后生成的 NLB 地址。</a-alert>
	      <a-alert v-else-if="domainDraft.access_type === 'ip'" type="info" show-icon>部署后到“资源与访问”复制网关 LoadBalancer 地址；请求按路径转发，不校验 Host。</a-alert>
	      <a-alert v-if="!loadingKubernetesServices && kubernetesServicesLoaded && !kubernetesServices.length" type="warning" show-icon style="margin-top:12px">当前 EKS 集群没有可用于转发的 Service，请先部署业务服务。</a-alert>
	    </a-form>
	  </a-modal>
	  <a-modal v-model:visible="channelVisible" title="新增告警通道" @before-ok="addChannel"><a-form :model="channelDraft" layout="vertical"><a-form-item label="名称" required><a-input v-model="channelDraft.name" /></a-form-item><a-form-item label="类型"><a-select v-model="channelDraft.type"><a-option value="lark">Lark</a-option><a-option value="feishu">飞书</a-option><a-option value="slack">Slack</a-option><a-option value="webhook">通用 Webhook</a-option><a-option value="telegram">TG（Telegram）</a-option><a-option value="dingtalk">钉钉</a-option><a-option value="wecom">企业微信</a-option><a-option value="email">Email</a-option></a-select></a-form-item><a-form-item label="接收地址" required :extra="channelDraft.type === 'email' ? '填写接收告警的邮箱地址。' : '直接填写完整的 HTTPS Webhook 地址；测试发送不允许访问本机或内网。'"><a-input v-model="channelDraft.address" :placeholder="channelDraft.type === 'email' ? 'ops@example.com' : 'https://example.com/webhook'" /></a-form-item><a-form-item label="认证凭据引用（可选）" extra="当地址不包含 Token、但发送时还需要认证时使用；支持 Secrets Manager ARN 或 namespace/secret/key。"><a-input v-model="channelDraft.secret_ref" placeholder="可留空" /></a-form-item></a-form></a-modal>
	  <a-modal v-model:visible="templateVisible" title="新增告警模板" width="720px" @before-ok="addTemplate"><a-form :model="templateDraft" layout="vertical"><a-grid :cols="2" :col-gap="12"><a-grid-item><a-form-item label="名称"><a-input v-model="templateDraft.name" /></a-form-item></a-grid-item><a-grid-item><a-form-item label="告警类型"><a-select v-model="templateDraft.event_type"><a-option value="cluster-control-plane">EKS 控制面监控</a-option><a-option value="kubernetes-workload">Kubernetes工作负载</a-option><a-option value="node-resource">节点资源</a-option><a-option value="deployment">部署任务</a-option><a-option value="database">数据库</a-option><a-option value="service-availability">服务可用性</a-option><a-option value="custom">自定义</a-option></a-select></a-form-item></a-grid-item><a-grid-item><a-form-item label="级别"><a-select v-model="templateDraft.severity"><a-option value="warning">Warning</a-option><a-option value="critical">Critical</a-option></a-select></a-form-item></a-grid-item><a-grid-item><a-form-item label="格式"><a-input model-value="Markdown" disabled /></a-form-item></a-grid-item><a-grid-item :span="2"><a-form-item label="标题模板"><a-input v-model="templateDraft.title" /></a-form-item></a-grid-item><a-grid-item :span="2"><a-form-item label="Markdown内容模板"><a-textarea v-model="templateDraft.body" :auto-size="{minRows:6,maxRows:14}" /></a-form-item></a-grid-item></a-grid></a-form></a-modal>
	  <a-modal v-model:visible="namespaceVisible" title="添加 Namespace" ok-text="添加" @before-ok="addNamespace"><a-form :model="{}" layout="vertical"><a-form-item label="Namespace 名称" required><a-input v-model="newNamespace" /></a-form-item></a-form></a-modal>
  <a-modal v-model:visible="deleteVisible" title="删除项目环境" :ok-text="deleteDestroyResources ? '开始销毁并删除' : '删除环境'" @before-ok="deleteEnvironment" @cancel="resetDeleteEnvironment">
    <a-alert :type="deleteDestroyResources ? (existingEKSTarget ? 'warning' : 'error') : 'warning'" show-icon>
      {{ deleteDestroyResources
        ? (existingEKSTarget ? '将卸载平台在该环境中创建的组件与接入资源；共享 EKS、Namespace、VPC 和节点组全部保留，成功后自动删除环境配置。' : '将先销毁该环境的 EKS、数据库、缓存、组件与 VPC；Terraform 状态确认为空后才自动删除环境配置。')
        : '不销毁任何 AWS 或 Kubernetes 资源。只有从未部署，或已销毁且 Terraform 状态为空的环境才能直接删除。' }}
    </a-alert>
    <a-form :model="{}" layout="vertical">
      <a-form-item label="先销毁已部署资源" :extra="!store.canDeploy ? '当前用户没有该项目的部署权限' : (!awsCredentialReady ? '当前项目需要先绑定 AWS 凭据' : '可选；关闭时只删除空环境配置')">
        <a-switch v-model="deleteDestroyResources" :disabled="!store.canDeploy || !awsCredentialReady" />
      </a-form-item>
      <a-form-item :label="`输入 ${scopeName}`"><a-input v-model="deleteConfirm" /></a-form-item>
      <template v-if="deleteDestroyResources">
        <a-form-item :label="`输入 destroy:${scopeName}`"><a-input v-model="deleteDestroyConfirm" /></a-form-item>
        <a-form-item label="当前账号密码"><a-input-password v-model="deletePassword" autocomplete="current-password" /></a-form-item>
      </template>
    </a-form>
  </a-modal>
  <a-modal v-model:visible="destroyVisible" :title="existingEKSTarget ? '卸载环境组件' : '销毁 AWS 资源'" ok-text="开始销毁" @before-ok="destroyEnvironment"><a-alert :type="existingEKSTarget ? 'warning' : 'error'">{{ existingEKSTarget ? '只删除平台在该环境中创建的 Helm 组件及相关资源；已有 EKS、Namespace、VPC 和节点组全部保留。仍需再次验证当前账号密码。' : '将销毁EKS、数据库、缓存、组件和VPC，此操作不可撤销。需要再次验证当前账号密码。' }}</a-alert><a-form :model="{}" layout="vertical"><a-form-item :label="`输入 destroy:${scopeName}`"><a-input v-model="destroyConfirm" /></a-form-item><a-form-item label="当前账号密码"><a-input-password v-model="destroyPassword" autocomplete="current-password" /></a-form-item></a-form></a-modal>
</template>

<script setup lang="ts">
import { computed, nextTick, onUnmounted, provide, reactive, ref, watch } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import { Message, Modal } from '@arco-design/web-vue';
import { IconDelete, IconDown, IconDownload, IconPlayArrow, IconPlus, IconRefresh, IconSave } from '@arco-design/web-vue/es/icon';
import { parse, stringify } from 'yaml';
import { usePlatformStore } from '@/stores/platform';
import { api } from '@/services/api';
import { deploymentActionState } from '@/services/deploymentAction';
import DataServiceCard from '@/components/DataServiceCard.vue';
import type { AWSEngineVersionResponse, AWSSecurityGroupInfo, AWSSecurityGroupResponse, AWSServiceInstanceOption, AWSServiceInstanceTypeResponse, AWSVPCInfo, AWSVPCResponse, AWSVPCSubnetInfo, ComponentConfig, Dict, EC2InstanceTypeInfo, EC2InstanceTypeResponse, EKSVersionInfo, EKSVersionResponse, HelmVersionOption, Job, JobAction, ManagedStorage, ManagedStorageReport } from '@/types';

type KubernetesServicePort = { name?: string; port: number; app_protocol?: string };
type KubernetesServiceOption = { name: string; namespace: string; type: string; ports: KubernetesServicePort[]; endpoint_health_known?: boolean; ready_endpoints?: number; total_endpoints?: number };
type KubernetesServiceResponse = { observed_at: string; services: KubernetesServiceOption[] };
type IngressConfigSyncResponse = {
  config: Dict;
  report: {
    updated_domains: number;
    imported_domains: number;
    imported_routes: number;
    consolidated_domains: number;
    preserved_domains: number;
    skipped: string[];
  };
};

const store = usePlatformStore(); const router = useRouter(); const route = useRoute(); const form = ref<Dict>({}); const activeTab = ref(String(route.query.tab || 'basic')); const saving = ref(false); const syncingAWSConfiguration = ref(false);
const formDirty = ref(false);
let dirtyWatchGeneration = 0;
let stopFormDirtyWatch: (() => void) | null = null;
const resetFormDirtyTracking = () => {
  dirtyWatchGeneration += 1;
  const generation = dirtyWatchGeneration;
  stopFormDirtyWatch?.();
  stopFormDirtyWatch = null;
  formDirty.value = false;
  // Arm after the configuration watcher finishes applying defaults. The deep
  // watcher stops after the first user change, avoiding a full traversal and
  // JSON serialization of the large deployment document on every render.
  void nextTick(() => {
    if (generation !== dirtyWatchGeneration) return;
    stopFormDirtyWatch = watch(form, () => {
      formDirty.value = true;
      stopFormDirtyWatch?.();
      stopFormDirtyWatch = null;
    }, { deep: true, flush: 'sync' });
  });
};
const nodeVisible = ref(false); const newNodeGroup = ref(''); const persistedNodeGroupNames = ref<Set<string>>(new Set()); const jobSubmitting = ref(false);
	const namespaceVisible = ref(false); const newNamespace = ref('');
const eksVersions = ref<EKSVersionInfo[]>([]); const loadingEKSVersions = ref(false); const eksVersionsError = ref('');
const vpcs = ref<AWSVPCInfo[]>([]); const loadingVPCs = ref(false); const vpcsError = ref('');
const securityGroups = ref<AWSSecurityGroupInfo[]>([]); const loadingSecurityGroups = ref(false); const securityGroupsError = ref('');
const instanceCatalogVisible = ref(false); const instanceCatalogGroup = ref(''); const instanceCatalogQuery = ref('m7i');
const instanceTypes = ref<EC2InstanceTypeInfo[]>([]); const loadingInstanceTypes = ref(false); const instanceCatalogError = ref('');
const deleteVisible = ref(false); const deleteConfirm = ref(''); const deleteDestroyResources = ref(false); const deleteDestroyConfirm = ref(''); const deletePassword = ref(''); const destroyVisible = ref(false); const destroyConfirm = ref(''); const destroyPassword = ref('');
const certificateVisible = ref(false); const certificateSaving = ref(false); const certificateIndex = ref(-1); const certificateDraft = reactive<Dict>({}); const tlsMaterialChanged = ref(false);
const domainVisible = ref(false); const domainIndex = ref(-1); const domainDraft = reactive<Dict>({});
const syncingDomains = ref(false);
const kubernetesServices = ref<KubernetesServiceOption[]>([]); const loadingKubernetesServices = ref(false); const kubernetesServicesLoaded = ref(false); const kubernetesServicesError = ref('');
const channelVisible = ref(false); const channelDraft = reactive<Dict>({ name: '', type: 'lark', address: '', secret_ref: '' }); const testingAlertChannel = ref(''); const testingAlertScenario = ref('');
const templateVisible = ref(false); const templateDraft = reactive<Dict>({ name: '', event_type: 'custom', severity: 'warning', format: 'markdown', title: '', body: '' });
const componentValuesVisible = ref(false); const componentValuesLoading = ref(false); const componentValuesTab = ref('form');
const componentValuesKey = ref(''); const componentValuesName = ref(''); const componentValuesYAML = ref('{}\n'); const componentValuesOriginal = ref('{}\n');
const componentValuesMessage = ref(''); const componentValuesError = ref(false);
const componentValuesDefaults = ref<Dict>({}); const componentValuesCandidatePaths = ref<string[]>([]);
const componentRemovalVisible = ref(false); const pendingComponentRemoval = ref<ComponentConfig | null>(null); const componentRemovalAcknowledged = ref(false);
const cloudServiceRemovalVisible = ref(false); const pendingCloudServiceRemovalKey = ref(''); const cloudServiceRemovalAcknowledged = ref(false);
const managedStorageItems = ref<ManagedStorage[]>([]); const loadingManagedStorage = ref(false); const managedStorageLoaded = ref(false);
const openTelemetryStorageItems = ref<ManagedStorage[]>([]); const loadingOpenTelemetryStorage = ref(false);
const openTelemetryWALStorageItems = computed(() => openTelemetryStorageItems.value.filter((item) => item.component === 'opentelemetry_collector'));
const openTelemetryElasticsearchStorageItems = computed(() => openTelemetryStorageItems.value.filter((item) => item.component === 'otel-elasticsearch'));
const managedStorageResizeVisible = ref(false); const managedStorageResizeTarget = ref<ManagedStorage | null>(null);
const managedStorageResizeOperation = ref<'expand' | 'shrink'>('expand'); const managedStorageTargetGi = ref(1);
const managedStorageSafetyPercent = ref(30); const managedStorageResizeAcknowledged = ref(false); const submittingManagedStorageResize = ref(false);
const componentAdvancedSelections = reactive<Record<string, string[]>>({});
const componentVersionCatalogs = reactive<Record<string, { loading: boolean; loaded: boolean; options: HelmVersionOption[]; error: string }>>({});
const cloudCatalogs = reactive<Record<string, { region: string; engineVersion: string; options: AWSServiceInstanceOption[]; loading: boolean; error: string }>>({});
const cloudEngineVersionCatalogs = reactive<Record<string, { region: string; options: string[]; loading: boolean; loaded: boolean; error: string }>>({});
const activeCloudService = ref('');
type DataServiceCredentialInfo = { service_key: string; username: string; configured: boolean; updated_by: string; updated_at: string };
const dataServiceCredentialInfos = reactive<Record<string, DataServiceCredentialInfo>>({});
const dataServicePasswords = reactive<Record<string, string>>({ rds: '', aurora: '' });
const loadingDataServiceCredentials = ref(false);
provide('activeCloudService', activeCloudService);
provide('cloudEngineVersionCatalog', {
  options: (service: string, engine: string, current: string) => cloudEngineVersionOptions(service, engine, current),
  loading: (service: string, engine: string) => cloudEngineVersionLoading(service, engine),
  hint: (service: string, engine: string) => cloudEngineVersionHint(service, engine),
  load: (service: string, engine: string, force = false) => loadCloudEngineVersions(service, engine, force),
});
const defaultAlertTemplates: Dict[] = [
  { name: 'cluster-control-plane-critical', event_type: 'cluster-control-plane', severity: 'critical', format: 'markdown', title: '{{ .StatusIcon }} {{ .StatusText }}｜EKS 控制面监控', body: `**影响范围**
项目：\`{{ .Project }}\`　环境：\`{{ .Environment }}\`
集群：\`{{ .Cluster }}\`
**告警详情**
级别：**{{ .SeverityText }}**　规则：\`{{ .AlertName }}\`
名称：{{ .AlertNameText }}
{{ if .MonitorTarget }}目标：\`{{ .MonitorTarget }}\`
{{ end }}{{ if .Instance }}实例：\`{{ .Instance }}\`
{{ end }}摘要：{{ .Summary }}
说明：{{ .Description }}
{{ if .OriginalMessage }}原始信息：{{ .OriginalMessage }}
{{ end }}**处理建议**
开始：{{ .StartsAt }}
{{ if .RunbookURL }}文档：[查看排查手册]({{ .RunbookURL }})
{{ end }}建议：{{ .Advice }}
{{ if .RelatedAlerts }}**同组其他告警**
{{ .RelatedAlerts }}
{{ end }}本组共 {{ .AlertCount }} 条` },
  { name: 'kubernetes-workload-critical', event_type: 'kubernetes-workload', severity: 'critical', format: 'markdown', title: '{{ .StatusIcon }} {{ .StatusText }}｜Kubernetes 工作负载', body: `**影响范围**
项目：\`{{ .Project }}\`　环境：\`{{ .Environment }}\`
集群：\`{{ .Cluster }}\`{{ if .Namespace }}　Namespace：\`{{ .Namespace }}\`{{ end }}
{{ if or .Workload .Pod .Container .Service }}{{ if .Workload }}工作负载：\`{{ .Workload }}\`
{{ end }}{{ if .Pod }}Pod：\`{{ .Pod }}\`
{{ end }}{{ if .Container }}容器：\`{{ .Container }}\`
{{ end }}{{ if .Service }}Service：\`{{ .Service }}\`
{{ end }}{{ end }}**告警详情**
级别：**{{ .SeverityText }}**　规则：\`{{ .AlertName }}\`
名称：{{ .AlertNameText }}
{{ if .MonitorTarget }}目标：\`{{ .MonitorTarget }}\`
{{ end }}摘要：{{ .Summary }}
说明：{{ .Description }}
{{ if .OriginalMessage }}原始信息：{{ .OriginalMessage }}
{{ end }}**处理建议**
开始：{{ .StartsAt }}
{{ if .RunbookURL }}文档：[查看排查手册]({{ .RunbookURL }})
{{ end }}建议：{{ .Advice }}
{{ if .RelatedAlerts }}**同组其他告警**
{{ .RelatedAlerts }}
{{ end }}本组共 {{ .AlertCount }} 条` },
  { name: 'node-resource-warning', event_type: 'node-resource', severity: 'warning', format: 'markdown', title: '{{ .StatusIcon }} {{ .StatusText }}｜EKS 节点资源', body: `**影响范围**
项目：\`{{ .Project }}\`　环境：\`{{ .Environment }}\`
集群：\`{{ .Cluster }}\`
{{ if .Node }}节点：\`{{ .Node }}\`
{{ end }}{{ if .Instance }}实例：\`{{ .Instance }}\`
{{ end }}**告警详情**
级别：**{{ .SeverityText }}**　规则：\`{{ .AlertName }}\`
{{ if .CurrentValue }}当前值：**{{ .CurrentValue }}**
{{ end }}{{ if .Threshold }}阈值：{{ .Threshold }}
{{ end }}摘要：{{ .Summary }}
说明：{{ .Description }}
{{ if .OriginalMessage }}原始信息：{{ .OriginalMessage }}
{{ end }}**处理建议**
开始：{{ .StartsAt }}
建议：{{ .Advice }}` },
  { name: 'deployment-failed-critical', event_type: 'deployment', severity: 'critical', format: 'markdown', title: '{{ .StatusIcon }} {{ .StatusText }}｜自动化部署任务', body: `**影响范围**
项目：\`{{ .Project }}\`　环境：\`{{ .Environment }}\`
集群：\`{{ .Cluster }}\`
{{ if .Stage }}阶段：{{ .Stage }}
{{ end }}{{ if .JobID }}任务：\`{{ .JobID }}\`
{{ end }}**失败详情**
级别：**{{ .SeverityText }}**　规则：\`{{ .AlertName }}\`
摘要：{{ .Summary }}
原因：{{ .Description }}
{{ if .OriginalMessage }}原始错误：{{ .OriginalMessage }}
{{ end }}**处理建议**
开始：{{ .StartsAt }}
建议：{{ .Advice }}` },
  { name: 'database-connection-critical', event_type: 'database', severity: 'critical', format: 'markdown', title: '{{ .StatusIcon }} {{ .StatusText }}｜数据库连接', body: `**影响范围**
项目：\`{{ .Project }}\`　环境：\`{{ .Environment }}\`
集群：\`{{ .Cluster }}\`
{{ if .Service }}服务：\`{{ .Service }}\`
{{ end }}{{ if .Engine }}引擎：\`{{ .Engine }}\`
{{ end }}**告警详情**
级别：**{{ .SeverityText }}**　规则：\`{{ .AlertName }}\`
{{ if .Duration }}持续：{{ .Duration }}
{{ end }}摘要：{{ .Summary }}
说明：{{ .Description }}
{{ if .OriginalMessage }}原始信息：{{ .OriginalMessage }}
{{ end }}**处理建议**
开始：{{ .StartsAt }}
建议：{{ .Advice }}` },
  { name: 'service-availability-warning', event_type: 'service-availability', severity: 'warning', format: 'markdown', title: '{{ .StatusIcon }} {{ .StatusText }}｜服务可用性', body: `**影响范围**
项目：\`{{ .Project }}\`　环境：\`{{ .Environment }}\`
集群：\`{{ .Cluster }}\`
{{ if .Service }}服务：\`{{ .Service }}\`
{{ end }}**告警详情**
级别：**{{ .SeverityText }}**　规则：\`{{ .AlertName }}\`
{{ if .HTTPStatus }}HTTP：\`{{ .HTTPStatus }}\`
{{ end }}{{ if .Availability }}可用率：**{{ .Availability }}**
{{ end }}摘要：{{ .Summary }}
说明：{{ .Description }}
{{ if .OriginalMessage }}原始信息：{{ .OriginalMessage }}
{{ end }}**处理建议**
开始：{{ .StartsAt }}
建议：{{ .Advice }}` },
];
const alertScenarios = [
  { key: 'cluster-control-plane', name: 'EKS 控制面监控', description: '托管控制面指标目标异常', severity: 'critical', symbol: 'EKS' },
  { key: 'kubernetes-workload', name: 'Kubernetes 工作负载', description: 'Pod 重启、工作负载异常', severity: 'critical', symbol: 'K8S' },
  { key: 'node-resource', name: '节点资源', description: 'CPU、内存与节点容量异常', severity: 'warning', symbol: 'NODE' },
  { key: 'deployment', name: '自动化部署', description: 'Terraform、Helm 或阶段任务失败', severity: 'critical', symbol: 'CD' },
  { key: 'database', name: '数据库连接', description: 'MySQL、PostgreSQL 等连接异常', severity: 'critical', symbol: 'DB' },
  { key: 'service-availability', name: '服务可用性', description: '网关错误率与服务可用率下降', severity: 'warning', symbol: 'SLA' },
  { key: 'recovery', name: '恢复通知', description: '验证恢复状态和绿色卡片', severity: 'normal', symbol: 'OK' },
] as const;
async function loadDataServiceCredentials() {
  const projectKey = store.currentProjectKey; const environmentKey = store.currentEnvironmentKey; const revision = store.scopeRevision;
  if (!projectKey || !environmentKey || loadingDataServiceCredentials.value) return;
  loadingDataServiceCredentials.value = true;
  try {
    const items = await api<DataServiceCredentialInfo[]>(`/api/projects/${encodeURIComponent(projectKey)}/environments/${encodeURIComponent(environmentKey)}/data-service-credentials`);
    if (revision !== store.scopeRevision) return;
    Object.keys(dataServiceCredentialInfos).forEach((key) => delete dataServiceCredentialInfos[key]);
    for (const item of items) dataServiceCredentialInfos[item.service_key] = item;
  } catch (error: any) {
    if (revision === store.scopeRevision) Message.warning(`数据库凭证状态加载失败：${error.message}`);
  } finally {
    if (revision === store.scopeRevision) loadingDataServiceCredentials.value = false;
  }
}
const dataServiceCredentialConfigured = (service: string) => Boolean(dataServiceCredentialInfos[service]?.configured);
const dataServiceCredentialHint = (service: string) => {
  if (loadingDataServiceCredentials.value) return '正在读取加密凭证状态…';
  const info = dataServiceCredentialInfos[service];
  return info?.configured ? `已加密保存（用户名 ${info.username}）；留空不修改密码。` : '尚未保存；密码不会出现在环境配置和部署日志中。';
};
watch(() => store.config, (value) => {
  stopFormDirtyWatch?.();
  stopFormDirtyWatch = null;
  form.value = value ? JSON.parse(JSON.stringify(value)) : {};
  persistedNodeGroupNames.value = new Set(Object.keys(value?.eks?.node_groups || {}));
  if (value) {
    if (!form.value.tls) form.value.tls = { certificates: [] };
    if (!Array.isArray(form.value.tls.certificates)) form.value.tls.certificates = [];
		if (!Array.isArray(form.value.network.workload_subnet_zones)) form.value.network.workload_subnet_zones = [...form.value.network.availability_zones];
		if (!Array.isArray(form.value.network.data_subnet_zones)) form.value.network.data_subnet_zones = [...form.value.network.availability_zones];
		if (!form.value.alerting) form.value.alerting = { enabled: false, namespace: 'monitoring', delivery_policy: 'core', channels: [], templates: [] };
		if (!form.value.alerting.delivery_policy) form.value.alerting.delivery_policy = 'core';
		if (!Array.isArray(form.value.alerting.templates)) form.value.alerting.templates = [];
		if (activeTab.value === 'eks') void loadEKSVersions();
			if (activeTab.value === 'network' && form.value.network.mode === 'existing') void loadVPCs();
			void loadDataServiceCredentials();
  }
  resetFormDirtyTracking();
}, { immediate: true });
watch(() => store.scopeRevision, () => {
  // Environment-specific editors and catalog responses must never survive a
  // project/environment switch.
  nodeVisible.value = false; newNodeGroup.value = '';
  persistedNodeGroupNames.value = new Set();
  namespaceVisible.value = false; newNamespace.value = '';
  eksVersions.value = []; eksVersionsError.value = ''; loadingEKSVersions.value = false;
  vpcs.value = []; vpcsError.value = ''; loadingVPCs.value = false;
  securityGroups.value = []; securityGroupsError.value = ''; loadingSecurityGroups.value = false;
  instanceCatalogVisible.value = false; instanceCatalogGroup.value = ''; instanceTypes.value = []; instanceCatalogError.value = ''; loadingInstanceTypes.value = false;
  deleteVisible.value = false; deleteConfirm.value = ''; deleteDestroyResources.value = false; deleteDestroyConfirm.value = ''; deletePassword.value = '';
  destroyVisible.value = false; destroyConfirm.value = ''; destroyPassword.value = '';
  certificateVisible.value = false; certificateSaving.value = false; certificateDraft.certificate_pem = ''; certificateDraft.private_key_pem = '';
  tlsMaterialChanged.value = false;
	  domainVisible.value = false; syncingDomains.value = false; kubernetesServices.value = []; loadingKubernetesServices.value = false; kubernetesServicesLoaded.value = false; kubernetesServicesError.value = ''; channelVisible.value = false; testingAlertChannel.value = ''; testingAlertScenario.value = ''; templateVisible.value = false;
  componentValuesVisible.value = false; componentValuesKey.value = ''; componentValuesMessage.value = ''; componentValuesLoading.value = false;
  componentRemovalVisible.value = false; pendingComponentRemoval.value = null; componentRemovalAcknowledged.value = false;
  cloudServiceRemovalVisible.value = false; pendingCloudServiceRemovalKey.value = ''; cloudServiceRemovalAcknowledged.value = false;
  componentValuesDefaults.value = {}; componentValuesCandidatePaths.value = [];
  managedStorageItems.value = []; loadingManagedStorage.value = false; managedStorageLoaded.value = false;
  openTelemetryStorageItems.value = []; loadingOpenTelemetryStorage.value = false; resetManagedStorageResize();
  Object.keys(componentAdvancedSelections).forEach((key) => delete componentAdvancedSelections[key]);
  Object.keys(componentVersionCatalogs).forEach((key) => delete componentVersionCatalogs[key]);
  Object.keys(cloudCatalogs).forEach((key) => delete cloudCatalogs[key]);
	  Object.keys(cloudEngineVersionCatalogs).forEach((key) => delete cloudEngineVersionCatalogs[key]);
	  Object.keys(dataServiceCredentialInfos).forEach((key) => delete dataServiceCredentialInfos[key]);
	  dataServicePasswords.rds = ''; dataServicePasswords.aurora = ''; loadingDataServiceCredentials.value = false;
	}, { flush: 'sync' });
watch(() => route.query.tab, (value) => { if (value) activeTab.value = String(value); });
onUnmounted(() => {
  dirtyWatchGeneration += 1;
  stopFormDirtyWatch?.();
  stopFormDirtyWatch = null;
});
const dirty = computed(() => tlsMaterialChanged.value || formDirty.value || Boolean(dataServicePasswords.rds || dataServicePasswords.aurora));
const existingEKSTarget = computed(() => form.value.deployment_target?.type === 'existing_eks');
const stageOneTabs = new Set(['basic', 'network', 'eks', 'data']);
const currentStage = computed<1 | 2>(() => existingEKSTarget.value || !stageOneTabs.has(activeTab.value) ? 2 : 1);
const canTestAlertScenarios = computed(() => Boolean(store.canConfigure && !dirty.value && form.value.alerting?.channels?.length && !testingAlertChannel.value && !testingAlertScenario.value));
const awsCredentialReady = computed(() => Boolean(
	store.awsCredential?.configured && store.awsCredential?.selected && store.awsCredential?.project_key === store.currentProjectKey,
));
const baseReady = computed(() => Boolean(store.status?.cluster.reachable));
const environmentBusy = computed(() => store.jobs.some((job) => ['queued', 'running'].includes(job.status)));
const cloudSync = computed(() => store.resources?.cloud_sync || null);
const cloudSyncWarnings = computed(() => (store.resources?.warnings || []).filter((warning) => warning.includes('实际参数读取失败')));
const cloudSyncAlertType = computed(() => ({ synced: 'success', pending: 'info', drifted: 'warning', conflict: 'error', unavailable: 'warning' }[cloudSync.value?.status || ''] || 'info') as any);
const cloudSyncTitle = computed(() => ({ synced: '平台配置与 AWS 实际配置一致', pending: '存在尚未部署的平台配置', drifted: '检测到用户在 AWS 控制台修改了资源', conflict: '平台修改与 AWS 控制台修改发生冲突', unavailable: 'AWS 实际参数读取不完整' }[cloudSync.value?.status || ''] || 'AWS 配置同步'));
const cloudSyncDescription = computed(() => {
  const value = cloudSync.value;
  if (!value) return '';
  if (value.blocking_changes) return `AWS 漂移 ${value.drifted_fields} 项、冲突 ${value.conflict_fields} 项。阶段1部署已被保护性拦截，请先查看差异并确定配置基线。`;
  if (value.pending_fields) return `${value.pending_fields} 项平台参数等待部署；AWS 实际状态未发生外部漂移。`;
  if (value.unavailable_resources) return `${value.unavailable_resources} 个资源暂时无法读取；平台不会用旧参数执行阶段1部署。`;
  return `已核对 ${value.synced_fields} 项 AWS 实际参数。`;
});
const canSyncDomains = computed(() => Boolean(
  store.currentProjectKey && store.currentEnvironmentKey && store.canConfigure &&
  !dirty.value && !environmentBusy.value && !syncingDomains.value,
));
const domainSyncHint = computed(() => {
  if (!store.canConfigure) return '当前用户没有该项目的配置修改权限';
  if (dirty.value) return '请先保存当前页面修改，再从 EKS 同步，避免覆盖未保存内容';
  if (environmentBusy.value) return '当前环境有部署任务执行中，请等待任务结束';
  return '读取当前项目环境可管理 Namespace 中的 Ingress；只更新平台配置，不修改 EKS';
});
// The backend derives these flags against the latest successful destroy
// boundary and the first real mutation step. Historical successful jobs must
// not turn a freshly destroyed environment back into an "update" operation.
const phaseOneDeployed = computed(() => Boolean(store.currentEnvironment?.phase_one_deployed));
const phaseTwoDeployed = computed(() => Boolean(store.currentEnvironment?.phase_two_deployed));
const nodePlanningLocked = computed(() => phaseOneDeployed.value || baseReady.value);
const nodeRoleOptions = [
	{ value: 'gateway', label: 'Ingress 网关组' },
	{ value: 'application', label: '业务服务组' },
	{ value: 'platform', label: '运维组件组' },
	{ value: 'stateful', label: '有状态服务组' },
	{ value: 'general', label: '通用节点组' },
] as const;
const deploymentAction = computed(() => deploymentActionState({
	stage: currentStage.value,
	deployed: currentStage.value === 1 ? phaseOneDeployed.value : phaseTwoDeployed.value,
	dirty: dirty.value,
	canConfigure: store.canConfigure,
	canDeploy: store.canDeploy,
	awsCredentialReady: awsCredentialReady.value,
	baseReady: baseReady.value,
	environmentBusy: environmentBusy.value,
	existingEKSTarget: existingEKSTarget.value,
}));
const accessOnlyDeployment = computed(() => phaseTwoDeployed.value && ['domains', 'alerts'].includes(activeTab.value));
const deploymentButtonLabel = computed(() => {
	if (!accessOnlyDeployment.value) return deploymentAction.value.label;
	return dirty.value ? '保存并更新接入配置' : '更新接入配置';
});
const amazonMQOptions = computed(() => cloudServiceOptions('amazon-mq').filter((option) =>
  !(option.deployment_modes || []).length || option.deployment_modes?.includes(form.value.data_services.amazon_mq.deployment_mode),
));
const componentNamespaceRows = computed(() => Object.keys(form.value.namespaces || {}).sort().map((name) => ({ name })));
const namespaceRows = computed(() => componentNamespaceRows.value.filter((item) => item.name !== 'platform-server'));
const kubernetesServiceNamespaces = computed(() => [...new Set(kubernetesServices.value.map((item) => item.namespace))].sort());
const domainServiceOptions = computed(() => kubernetesServices.value.filter((item) => item.namespace === String(domainDraft.namespace || '')));
const selectedDomainService = computed(() => domainServiceOptions.value.find((item) => item.name === String(domainDraft.service || '')));
const domainServicePorts = computed(() => selectedDomainService.value?.ports || []);
const domainNamespaceMissing = computed(() => Boolean(domainDraft.namespace && kubernetesServicesLoaded.value && !kubernetesServiceNamespaces.value.includes(String(domainDraft.namespace))));
const domainServiceMissing = computed(() => Boolean(domainDraft.service && kubernetesServicesLoaded.value && !selectedDomainService.value));
const domainPortMissing = computed(() => Boolean(domainDraft.service_port && kubernetesServicesLoaded.value && !domainServicePorts.value.some((item) => item.port === Number(domainDraft.service_port))));
const rawTCPServicePorts = new Set([2379, 2380, 3306, 5432, 5672, 6379, 9092, 27017, 61616]);
const routeProtocol = (record: Dict) => String(record.protocol || (record.tls_enabled ? 'https' : 'http')).toLowerCase();
const routeIsSecure = (record: Dict) => ['https', 'wss', 'grpcs'].includes(routeProtocol(record));
const domainRoutes = (record: Dict): Dict[] => {
  if (routeProtocol(record) === 'tcp') return [record];
  if (Array.isArray(record.routes) && record.routes.length) return record.routes;
  return [{ path: record.path || '/', path_type: record.path_type || 'Prefix', service: record.service, service_port: record.service_port }];
};
const totalDomainRouteCount = computed(() => (form.value.domains || []).reduce((total: number, domain: Dict) => total + domainRoutes(domain).length, 0));
const routeProtocolLabel = (record: Dict) => {
  const labels: Record<string, string> = { http: 'HTTP', https: 'HTTPS', ws: 'WebSocket', wss: 'WSS', grpc: 'gRPC', grpcs: 'gRPCS', tcp: 'TCP' };
  return labels[routeProtocol(record)] || routeProtocol(record).toUpperCase();
};
const inferBackendProtocol = (port?: KubernetesServicePort) => {
  if (!port) return 'http';
  const appProtocol = String(port.app_protocol || '').toLowerCase();
  const name = String(port.name || '').toLowerCase();
  if (appProtocol.includes('h2c') || appProtocol.includes('grpc') || /(^|[-_.])(grpc|rpc|h2c)([-_.]|$)/.test(name)) return 'grpc';
  return appProtocol.includes('https') || /(^|[-_.])(https|tls)([-_.]|$)/.test(name) ? 'https' : 'http';
};
const isTCPRoute = computed(() => routeProtocol(domainDraft) === 'tcp');
const isGRPCRoute = computed(() => ['grpc', 'grpcs'].includes(routeProtocol(domainDraft)));
const kubernetesServicesHint = computed(() => {
  if (loadingKubernetesServices.value) return '正在从当前项目环境的 EKS 集群读取 Service…';
  if (kubernetesServicesError.value) return kubernetesServicesError.value;
  if (!kubernetesServicesLoaded.value) return '打开配置时会从 EKS 实时读取，不使用手工填写值。';
  return `已从 EKS 读取 ${kubernetesServices.value.length} 个 Service，选择 Service 后自动带出端口。`;
});
const serviceEndpointLabel = (service: KubernetesServiceOption) => {
  if (service.type === 'ExternalName') return '外部名称';
  if (!service.endpoint_health_known) return '后端状态未知';
  if ((service.ready_endpoints || 0) === 0) return '无可用后端';
  return `健康 ${service.ready_endpoints}/${service.total_endpoints || service.ready_endpoints}`;
};
const subnetRows = computed(() => (form.value.network?.availability_zones || []).map((zone: string) => ({ zone })));
const selectedVPC = computed(() => vpcs.value.find((item) => item.id === form.value.network?.existing_vpc_id));
const selectedVPCSubnets = computed(() => selectedVPC.value?.subnets || []);
const nodeGroups = computed(() => Object.entries(form.value.eks?.node_groups || {}) as Array<[string, Dict]>);
const isPersistedNodeGroup = (name: string) => persistedNodeGroupNames.value.has(name);
const nodeGroupFieldLocked = (name: string) => nodePlanningLocked.value && isPersistedNodeGroup(name);
const nodeGroupActualDesired = (name: string, group: Dict): number | null => {
  if (!isPersistedNodeGroup(name)) return null;
  const eksResource = store.resources?.resources?.find((item) => item.key === 'eks');
  const field = eksResource?.configuration?.find((item) => item.path === `eks.node_groups.${name}.desired_size`);
  const actual = Number(field?.actual);
  return Number.isFinite(actual) ? actual : (Number.isFinite(Number(group.desired_size)) ? Number(group.desired_size) : null);
};
const scopeName = computed(() => `${store.currentProjectKey}/${store.currentEnvironmentKey}`);
	type CloudServiceChoice = { key: string; title: string; short: string; description: string; model: Dict };
	const cloudServiceChoices = computed<CloudServiceChoice[]>(() => {
	  const services = form.value.data_services || {};
	  return [
	    { key: 'rds', title: 'RDS MySQL', short: 'MY', description: '管理后台关系型数据库', model: services.rds },
	    { key: 'aurora', title: 'Aurora MySQL', short: 'AU', description: '游戏数据高可用集群', model: services.aurora },
	    { key: 'postgres', title: 'RDS PostgreSQL', short: 'PG', description: 'PostgreSQL 托管实例', model: services.postgres },
	    { key: 'documentdb', title: 'Amazon DocumentDB', short: 'DB', description: 'MongoDB 兼容托管集群', model: services.documentdb },
	    { key: 'elasticache', title: 'AWS ElastiCache（Redis / Valkey）', short: 'RD', description: 'AWS 托管 Redis / Valkey 缓存', model: services.elasticache },
	    { key: 'msk', title: 'Amazon MSK Kafka', short: 'KF', description: '托管 Kafka 消息集群', model: services.msk },
	    { key: 'amazon_mq', title: 'Amazon MQ RabbitMQ', short: 'MQ', description: '托管 RabbitMQ 消息队列', model: services.amazon_mq },
	    { key: 'ecr', title: 'Amazon ECR 镜像仓库', short: 'ECR', description: '容器镜像仓库与生命周期', model: form.value.ecr },
	  ].filter((item) => item.model && typeof item.model === 'object') as CloudServiceChoice[];
	});
	const enabledCloudServices = computed(() => cloudServiceChoices.value.filter((item) => Boolean(item.model.enabled)));
	const availableCloudServices = computed(() => cloudServiceChoices.value.filter((item) => !item.model.enabled));
	const elasticacheTotalNodes = computed(() => {
	  const cache = form.value.data_services?.elasticache || {};
	  if (cache.mode === 'serverless') return 0;
		return Math.max(0, Number(cache.num_node_groups || 0)) * Math.max(0, Number(cache.nodes_per_shard || 0));
	});
	const dataSubnetZoneCount = computed(() => {
		const network = form.value.network || {};
		const selected = network.mode === 'existing' ? network.existing_data_subnet_ids : network.data_subnet_zones;
		return Math.max(2, Array.isArray(selected) ? selected.length : 0);
	});
	const enableCloudService = (service: CloudServiceChoice) => { service.model.enabled = true; activeCloudService.value = service.key; };
	const pendingCloudServiceRemoval = computed(() => cloudServiceChoices.value.find((item) => item.key === pendingCloudServiceRemovalKey.value));
	const resetCloudServiceRemoval = () => { cloudServiceRemovalVisible.value = false; pendingCloudServiceRemovalKey.value = ''; cloudServiceRemovalAcknowledged.value = false; };
	const requestCloudServiceToggle = (key: string, value: boolean) => {
		const service = cloudServiceChoices.value.find((item) => item.key === key);
		if (!service) return;
		if (value) { service.model.enabled = true; activeCloudService.value = key; return; }
		if (key !== 'ecr' && Boolean(service.model.deletion_protection)) {
			Message.warning(`${service.title} 已开启删除保护；请先关闭删除保护、保存配置，再标记删除`);
			return;
		}
		pendingCloudServiceRemovalKey.value = key; cloudServiceRemovalAcknowledged.value = false; cloudServiceRemovalVisible.value = true;
	};
	provide('requestCloudServiceToggle', requestCloudServiceToggle);
	const cloudServiceRemovalSummary = computed(() => pendingCloudServiceRemovalKey.value === 'ecr'
		? '关闭 ECR 只会停止当前环境的仓库策略对账；项目共享仓库和已有镜像不会被删除。'
		: '执行阶段 1 后，Terraform 会删除该 AWS 云服务。服务地址将失效，业务连接会中断。');
	const cloudServiceRemovalDataPolicy = computed(() => {
		const service = pendingCloudServiceRemoval.value; if (!service) return '-';
		if (service.key === 'ecr') return '项目共享 ECR 仓库和镜像全部保留。';
		if (['rds', 'aurora', 'postgres', 'documentdb'].includes(service.key)) return service.model.skip_final_snapshot === false ? '删除前按当前 Terraform 配置创建最终快照。' : '已配置跳过最终快照，在线数据将随资源删除。';
		if (service.key === 'elasticache') return '删除 Replication Group/Serverless Cache；已有手工快照按 AWS 保留策略保留。';
		if (service.key === 'msk') return '删除 MSK 集群及其 Broker 存储；主题数据不保留。';
		if (service.key === 'amazon_mq') return '删除 Amazon MQ Broker；队列和未消费消息不保留。';
		return '删除该服务的在线数据。';
	});
	const cloudServiceRemovalProtection = computed(() => {
		const service = pendingCloudServiceRemoval.value; if (!service) return '-';
		if (service.key === 'ecr') return '无需解除删除保护，因为不执行仓库删除。';
		return service.model.deletion_protection ? '删除保护已开启，平台已阻止本次操作。' : '删除保护已关闭，允许标记删除。';
	});
	const confirmCloudServiceRemoval = () => {
		const service = pendingCloudServiceRemoval.value;
		if (!service || !cloudServiceRemovalAcknowledged.value) { Message.warning('请先确认云服务删除和数据处理影响'); return false; }
		service.model.enabled = false;
		Message.warning(`已标记${service.key === 'ecr' ? '停止对账' : '删除'} ${service.title}；请保存配置并执行阶段 1`);
		resetCloudServiceRemoval();
		return true;
	};
	watch(() => enabledCloudServices.value.map((item) => item.key).join(','), () => {
	  if (!enabledCloudServices.value.some((item) => item.key === activeCloudService.value)) activeCloudService.value = enabledCloudServices.value[0]?.key || '';
	}, { immediate: true });
	const privateNetworkRequired = computed(() => form.value.network?.workload_subnet_type === 'private' || form.value.network?.data_subnet_type === 'private');
	const natGatewayHint = computed(() => {
	  if (form.value.network?.nat_gateway_mode === 'always') return '立即创建 NAT 与固定 EIP，并为 Private 子网配置默认路由；不改变 Public 子网路由。';
	  if (form.value.network?.nat_gateway_mode === 'disabled') return privateNetworkRequired.value ? '当前使用 Private 网络，关闭 NAT 后工作负载可能无法访问公网。' : '不会创建 NAT，Private 子网没有默认公网出口。';
	  return privateNetworkRequired.value ? '已选择 Private 网络，本次将创建 NAT。' : '当前均为 Public 网络，暂不创建 NAT。';
	});
	const visibleComponents = computed(() => {
	  const builtins = (store.platform?.components || []).filter((item) => !item.hidden);
	  const builtinKeys = new Set(builtins.map((item) => item.key));
	  const extensions: ComponentConfig[] = store.componentCatalog.filter((item) => !builtinKeys.has(item.key)).map((item) => ({
	    key: item.key, display_name: item.display_name, category: item.category, description: item.description,
	    config_path: `components.catalog.${item.key}.enabled`, stage: 'platform', status_type: 'helm', status_name: item.key, hidden: false,
	  }));
	  return [...builtins, ...extensions];
	});
const baseServiceComponents = computed(() => visibleComponents.value.filter((item) => ['consul', 'etcd'].includes(item.key)));
const stageTwoComponents = computed(() => visibleComponents.value.filter((item) => !['consul', 'etcd'].includes(item.key)));
const catalogComponents = computed(() => stageTwoComponents.value.filter((item) => item.config_path.startsWith('components.catalog.') && componentEnabled(item.config_path)));
const statefulBaseComponents = computed(() => baseServiceComponents.value);
const enabledComponentNames = computed(() => visibleComponents.value.filter((item) => componentEnabled(item.config_path)).map((item) => item.display_name).join('、'));
const tlsRequiresComponentPhase = computed(() => (form.value.tls?.certificates || []).some((item: Dict) => item.enabled !== false && item.mode === 'cert-manager'));
const enabledSummary = computed(() => {
  if (currentStage.value === 1) {
    const cloudServices = Object.values(form.value.data_services || {}).filter((value: any) => value.enabled).length;
    return `阶段1 · 云中间件与云数据库 ${cloudServices} 项 · 节点组 ${nodeGroups.value.length} 个`;
  }
  if (activeTab.value === 'tls') {
    const certificates = form.value.tls?.certificates || [];
    const uploaded = certificates.filter((item: Dict) => item.enabled !== false && item.mode === 'uploaded-pem').length;
    return `阶段2 · TLS证书 ${certificates.length} 项 · 粘贴证书 ${uploaded} 项`;
  }
  return `阶段2 · 自建组件 ${stageTwoComponents.value.filter((item) => componentEnabled(item.config_path)).length} 项 · 域名 ${(form.value.domains || []).length} 个 · 路由 ${totalDomainRouteCount.value} 条`;
});
const stageReadiness = computed(() => {
  if (!awsCredentialReady.value) return '请先绑定当前项目的 AWS 凭据';
	  if (environmentBusy.value) return dirty.value ? '当前任务正在执行；已识别新修改，任务结束后可保存并更新部署' : '当前环境有任务正在执行';
	  if (dirty.value) return activeTab.value === 'tls' ? '保存后将自动应用 TLS 配置' : `已识别未保存修改；点击“${deploymentButtonLabel.value}”会先保存再部署`;
  if (currentStage.value === 1) return `${phaseOneDeployed.value ? '阶段一已有受管资源，可以更新部署' : '可以开始阶段一部署'}；任务内会先生成执行计划`;
  if (!baseReady.value) return existingEKSTarget.value ? '等待已有 EKS 接入检查' : '阶段 2 等待 EKS 就绪';
  if (activeTab.value === 'tls') return 'TLS 配置已保存；修改后再次保存会自动应用';
  return phaseTwoDeployed.value ? '阶段二已有受管组件，可以更新部署' : existingEKSTarget.value ? '可以开始部署到已有 EKS' : '可以开始阶段二部署';
});
const cloudServiceOptions = (service: string) => {
  const catalog = cloudCatalogs[service];
  return catalog?.region === form.value.region ? catalog.options : [];
};
const cloudEngineVersionKey = (service: string, engine = '') => `${service}:${engine || '-'}`;
const builtinCloudEngineVersions = (service: string, engine = '') => {
  if (service === 'rds-mysql') return ['8.4', '8.0', '5.7'];
  if (service === 'rds-postgres') return ['17', '16', '15', '14', '13'];
  if (service === 'aurora-mysql') return ['8.0.mysql_aurora.3.12.0', '8.0.mysql_aurora.3.11.1', '8.0.mysql_aurora.3.10.4', '8.0.mysql_aurora.3.10.3'];
  if (service === 'documentdb') return ['8.0.0', '5.0.1', '5.0.0', '4.0.0'];
  if (service === 'elasticache' && engine === 'redis') return ['7.1', '7.0', '6.2', '6.0', '5.0.6'];
  if (service === 'elasticache') return ['9.1', '9.0', '8.2', '8.1', '8.0', '7.2'];
  if (service === 'msk') return ['4.1.x', '4.0.x', '3.9.x', '3.8.x', '3.7.x'];
  if (service === 'amazon-mq') return ['4.2', '3.13'];
  return [];
};
const cloudEngineVersionOptions = (service: string, engine: string, current: string) => {
  const catalog = cloudEngineVersionCatalogs[cloudEngineVersionKey(service, engine)];
  return [...new Set([current, ...(catalog?.region === form.value.region ? catalog.options : []), ...builtinCloudEngineVersions(service, engine)].filter(Boolean))];
};
const cloudEngineVersionLoading = (service: string, engine: string) => Boolean(cloudEngineVersionCatalogs[cloudEngineVersionKey(service, engine)]?.loading);
const cloudEngineVersionHint = (service: string, engine: string) => {
  const catalog = cloudEngineVersionCatalogs[cloudEngineVersionKey(service, engine)];
  if (catalog?.loading) return `正在从 AWS 查询 ${form.value.region} 可用引擎版本…`;
  if (catalog?.error) return `${catalog.error}；已显示平台内置兼容版本`;
  if (catalog?.loaded && catalog.region === form.value.region) return `AWS 实时返回 ${catalog.options.length} 个可用版本`;
  return '展开下拉框时从 AWS 实时查询当前 Region 可用版本';
};
const loadCloudEngineVersions = async (service: string, engine = '', force = false) => {
  const projectKey = store.currentProjectKey; const region = String(form.value.region || ''); const key = cloudEngineVersionKey(service, engine); const current = cloudEngineVersionCatalogs[key];
  if (!projectKey || !region || current?.loading || (!force && current?.loaded && current.region === region)) return;
  cloudEngineVersionCatalogs[key] = { region, options: current?.region === region ? current.options : [], loading: true, loaded: false, error: '' };
  const revision = store.scopeRevision;
  try {
    const query = new URLSearchParams({ region, service }); if (engine) query.set('engine', engine);
    const response = await api<AWSEngineVersionResponse>(`/api/projects/${encodeURIComponent(projectKey)}/aws-catalog/engine-versions?${query}`);
    if (revision !== store.scopeRevision || form.value.region !== region) return;
    cloudEngineVersionCatalogs[key] = { region, options: response.versions || [], loading: false, loaded: true, error: '' };
  } catch (error: any) {
    if (revision !== store.scopeRevision) return;
    cloudEngineVersionCatalogs[key] = { region, options: current?.region === region ? current.options : [], loading: false, loaded: true, error: error.message };
  }
};
const cloudCatalogLoading = (service: string) => Boolean(cloudCatalogs[service]?.loading);
const cloudCatalogError = (service: string) => cloudCatalogs[service]?.error || '';
const cloudCatalogHint = (service: string, source: string) => {
  const catalog = cloudCatalogs[service];
  if (catalog?.loading) return `正在从 ${source} 查询 ${form.value.region} 可用规格…`;
  if (catalog?.error) return catalog.error;
  if (catalog?.region === form.value.region && catalog.options.length) return `${source} 实时返回 ${catalog.options.length} 个当前 Region 可用规格`;
  return `展开选择框或点击刷新，从 ${source} 实时查询当前 Region`;
};
const cloudCurrentMissing = (service: string, current: string) => {
  if (!current) return false;
  const options = service === 'amazon-mq' ? amazonMQOptions.value : cloudServiceOptions(service);
  return !options.some((option) => option.value === current);
};
const subnetLabel = (subnet: AWSVPCSubnetInfo) => `${subnet.name || '未命名'} · ${subnet.id} · ${subnet.availability_zone} · ${subnet.cidr} · 可用IP ${subnet.available_ip_count}${subnet.map_public_ip_on_launch ? ' · 自动公网IP' : ''}`;
const loadVPCs = async (force = false) => {
  const projectKey = store.currentProjectKey; const region = String(form.value.region || ''); const revision = store.scopeRevision;
  if (!projectKey || !region || loadingVPCs.value || (!force && vpcs.value.length)) return;
  loadingVPCs.value = true; vpcsError.value = '';
  try {
    const response = await api<AWSVPCResponse>(`/api/projects/${encodeURIComponent(projectKey)}/aws-catalog/vpcs?region=${encodeURIComponent(region)}`);
    if (revision !== store.scopeRevision || form.value.region !== region) return;
    vpcs.value = response.vpcs || [];
    const current = vpcs.value.find((item) => item.id === form.value.network.existing_vpc_id);
    if (current) form.value.network.existing_vpc_cidr = current.cidr;
    else if (form.value.network.existing_vpc_id) vpcsError.value = `当前凭据在 ${region} 未找到已配置的 VPC ${form.value.network.existing_vpc_id}`;
    else if (!vpcs.value.length) vpcsError.value = `AWS 在 ${region} 没有返回可用 VPC`;
  } catch (error: any) {
    if (revision === store.scopeRevision) { vpcs.value = []; vpcsError.value = error.message; }
  } finally { if (revision === store.scopeRevision) loadingVPCs.value = false; }
};
const normalizeExistingSubnets = (key: 'existing_workload_subnet_ids' | 'existing_data_subnet_ids') => {
  const ids = Array.isArray(form.value.network[key]) ? form.value.network[key] : [];
  const selected = ids.map((id: string) => selectedVPCSubnets.value.find((item) => item.id === id)).filter(Boolean) as AWSVPCSubnetInfo[];
  const zones = new Set<string>(); const normalized: string[] = [];
  for (const subnet of selected) {
    if (normalized.length >= 3 || zones.has(subnet.availability_zone)) continue;
    zones.add(subnet.availability_zone); normalized.push(subnet.id);
  }
  if (normalized.length !== ids.length) Message.warning('同一用途最多选择 3 个子网，且每个可用区只能选择 1 个');
  form.value.network[key] = normalized;
  return [...zones].sort();
};
const syncExistingWorkloadZones = () => {
  const zones = normalizeExistingSubnets('existing_workload_subnet_ids');
  form.value.network.availability_zones = zones;
  form.value.network.workload_subnet_zones = [...zones];
  for (const group of Object.values(form.value.eks.node_groups || {}) as Dict[]) group.availability_zones = [...zones];
};
const syncExistingDataZones = () => { form.value.network.data_subnet_zones = normalizeExistingSubnets('existing_data_subnet_ids'); };
const selectExistingVPC = (value: unknown) => {
  const vpc = vpcs.value.find((item) => item.id === String(value));
  form.value.network.existing_vpc_cidr = vpc?.cidr || '';
  const allowed = new Set((vpc?.subnets || []).map((item) => item.id));
  form.value.network.existing_workload_subnet_ids = (form.value.network.existing_workload_subnet_ids || []).filter((id: string) => allowed.has(id));
  form.value.network.existing_data_subnet_ids = (form.value.network.existing_data_subnet_ids || []).filter((id: string) => allowed.has(id));
  syncExistingWorkloadZones(); syncExistingDataZones();
  securityGroups.value = []; securityGroupsError.value = '';
  if (higressUsesCustomSecurityGroups.value) void loadSecurityGroups(true);
};
const changeNetworkMode = (value: unknown) => {
  if (String(value) === 'existing') { void loadVPCs(); return; }
	if (higressUsesCustomSecurityGroups.value) {
		const higress = catalogConfig('higress');
		if (!higress.nlb || typeof higress.nlb !== 'object' || Array.isArray(higress.nlb)) higress.nlb = {};
		higress.nlb.security_group_mode = 'managed';
		higress.nlb.security_group_ids = [];
		securityGroups.value = [];
		securityGroupsError.value = '';
		Message.info('已切换为新建 VPC，Higress NLB 自动恢复为平台管理安全组');
	}
  remapZones(String(form.value.region));
};
const loadCloudServiceOptions = async (service: string, engineVersion = '', force = false) => {
  const region = String(form.value.region || '');
  const projectKey = store.currentProjectKey;
  const revision = store.scopeRevision;
  const current = cloudCatalogs[service];
  if (!region || current?.loading) return;
  if (!force && current?.region === region && current.engineVersion === engineVersion && current.options.length) return;
  cloudCatalogs[service] = { region, engineVersion, options: current?.region === region ? current.options : [], loading: true, error: '' };
  try {
    const query = new URLSearchParams({ region, service });
    if (engineVersion) query.set('engine_version', engineVersion);
    const response = await api<AWSServiceInstanceTypeResponse>(`/api/projects/${encodeURIComponent(projectKey)}/aws-catalog/service-instance-types?${query}`);
    if (revision !== store.scopeRevision || form.value.region !== region) return;
    cloudCatalogs[service] = { region, engineVersion, options: response.instance_types || [], loading: false, error: response.instance_types?.length ? '' : `AWS 在 ${region} 没有返回可用规格` };
  } catch (error: any) {
    if (revision !== store.scopeRevision || form.value.region !== region) return;
    cloudCatalogs[service] = { region, engineVersion, options: current?.region === region ? current.options : [], loading: false, error: error.message };
  }
};
function getPath(path: string) { return path.split('.').reduce((value: any, key) => value?.[key], form.value); }
function setPath(path: string, value: any) { const keys = path.split('.'); let target = form.value; for (const key of keys.slice(0, -1)) { if (!target[key]) target[key] = {}; target = target[key]; } target[keys[keys.length - 1]] = value; }
function getObjectPath(source: Dict, path: string) { return path.split('.').reduce((value: any, key) => value?.[key], source); }
function setObjectPath(source: Dict, path: string, value: any) { const keys = path.split('.'); let target = source; for (const key of keys.slice(0, -1)) { if (!target[key] || typeof target[key] !== 'object' || Array.isArray(target[key])) target[key] = {}; target = target[key]; } target[keys[keys.length - 1]] = value; }
const componentEnabled = (path: string) => Boolean(getPath(path));
const componentRuntimeConfig = (component: ComponentConfig) => getPath(component.config_path.replace(/\.enabled$/, '')) || {};
const componentWebUIConfig = (component: ComponentConfig) => {
  const config = componentRuntimeConfig(component);
  if (!config.web_ui || typeof config.web_ui !== 'object' || Array.isArray(config.web_ui)) config.web_ui = { enabled: true, username: 'admin' };
  if (config.web_ui.enabled === undefined) config.web_ui.enabled = true;
  return config.web_ui;
};
const componentMode = (component: ComponentConfig) => String(componentRuntimeConfig(component).deployment_mode || (Number(componentRuntimeConfig(component).replicas || 1) > 1 ? 'cluster' : 'standalone'));
const baseComponentImageOptions = (component: ComponentConfig) => {
  const current = String(componentRuntimeConfig(component).image || '');
  const supported = component.key === 'consul'
    ? ['hashicorp/consul:1.21.3', 'hashicorp/consul:1.20.6', 'hashicorp/consul:1.19.7']
    : ['quay.io/coreos/etcd:v3.6.4', 'quay.io/coreos/etcd:v3.5.21', 'quay.io/coreos/etcd:v3.5.20'];
  return [...new Set([current, ...supported].filter(Boolean))];
};
const imageVersionLabel = (image: string) => { const index = image.lastIndexOf(':'); return index > image.lastIndexOf('/') ? image.slice(index + 1).replace(/^v/, '') : image; };
const componentHasReplicaPaths = (component: ComponentConfig) => Array.isArray(componentRuntimeConfig(component).replica_paths) && componentRuntimeConfig(component).replica_paths.length > 0;
const componentStandaloneOnly = (component: ComponentConfig) => Boolean(componentRuntimeConfig(component).standalone_only);
const componentHasDirectReplicas = (component: ComponentConfig) => ['consul', 'etcd'].includes(component.key);
const componentReplicaHint = (component: ComponentConfig) => {
	if (componentMode(component) !== 'cluster') return '单机模式固定为 1 副本。';
	if (componentHasDirectReplicas(component)) return '基础有状态服务集群模式建议使用 3、5 或 7 个奇数副本。';
	if (componentHasReplicaPaths(component)) return '集群模式建议至少 3 副本；平台会写入组件目录声明的 Helm 副本参数。';
  if (component.key === 'jenkins') return 'Jenkins 集群模式使用 Kubernetes 动态 Agent 横向扩展，控制器保持单副本。';
  if (component.key === 'tekton') return 'Tekton 按 PipelineRun 动态创建任务 Pod，控制器保持官方 Chart 拓扑。';
  return '该 Chart 未声明控制面副本参数；集群模式按组件原生工作负载模型运行。';
};
const setComponentMode = (component: ComponentConfig, mode: string) => {
  const config = componentRuntimeConfig(component);
	if (component.key === 'jaeger' && mode === 'cluster' && String(componentValue('jaeger', 'storage.backend', 'badger')) === 'badger') {
		Message.warning('Jaeger Badger 仅支持单机；请先在下方切换为 Elasticsearch 存储');
		config.deployment_mode = 'standalone'; config.replicas = 1; return;
	}
	if (componentStandaloneOnly(component) && mode === 'cluster') {
		Message.warning(`${component.display_name} 当前使用内置存储，仅支持单机模式`);
		config.deployment_mode = 'standalone'; config.replicas = 1; return;
	}
  config.deployment_mode = mode === 'cluster' ? 'cluster' : 'standalone';
  config.replicas = config.deployment_mode === 'cluster' && (componentHasReplicaPaths(component) || componentHasDirectReplicas(component)) ? Math.max(3, Number(config.replicas || 0)) : 1;
};
const setComponent = (path: string, value: boolean) => {
	if (path === 'components.catalog.mysql.enabled' && !value && componentEnabled('components.catalog.bytebase.enabled')) {
		Message.warning('Bytebase 正在自动管理本环境 MySQL；请先关闭 Bytebase'); return;
	}
	if (path === 'components.catalog.redis.enabled' && !value && componentEnabled('components.catalog.redisinsight.enabled')) {
		Message.warning('RedisInsight 正在自动管理本环境 Redis；请先关闭 RedisInsight'); return;
	}
	if (path === 'components.etcd.enabled' && !value && componentEnabled('components.catalog.etcd_workbench.enabled')) {
		Message.warning('Etcd Workbench 依赖本环境 etcd；请先关闭 Etcd Workbench'); return;
	}
	if (path === 'components.catalog.prometheus.enabled' && !value && componentEnabled('components.catalog.loki.enabled')) {
		Message.warning('Loki 需要 Grafana 作为日志查询界面；请先关闭 Loki，再关闭 Prometheus + Grafana');
		return;
	}
	if (!value && path === 'components.catalog.jaeger.enabled' && componentEnabled('components.catalog.opentelemetry_collector.enabled') && Boolean(componentValue('opentelemetry_collector', 'destinations.jaeger.enabled', true))) {
		Message.warning('OpenTelemetry 正在向 Jaeger 写入 Trace；请先关闭 Jaeger 出口，再卸载 Jaeger');
		return;
	}
	if (!value && path === 'components.catalog.tempo.enabled' && componentEnabled('components.catalog.opentelemetry_collector.enabled') && Boolean(componentValue('opentelemetry_collector', 'destinations.tempo.enabled', false))) {
		Message.warning('OpenTelemetry 正在向 Tempo 写入 Trace；请先关闭 Tempo 出口，再卸载 Tempo');
		return;
	}
	if (!value && ['components.catalog.prometheus.enabled', 'components.catalog.loki.enabled'].includes(path) && componentEnabled('components.catalog.opentelemetry_collector.enabled')) {
		Message.warning('OpenTelemetry 统一采集依赖 Prometheus 和 Loki；请先关闭 OpenTelemetry Collector');
		return;
	}
  setPath(path, value);
  if (!value || !path.endsWith('.enabled')) return;
	if (path === 'components.catalog.loki.enabled') {
		setPath('components.catalog.prometheus.enabled', true);
		Message.info('已同时启用 Prometheus + Grafana；部署时会自动采集 EKS 日志并创建 Loki 数据源');
	}
	if (path === 'components.catalog.opentelemetry_collector.enabled') {
		for (const dependency of ['prometheus', 'loki', 'jaeger']) setPath(`components.catalog.${dependency}.enabled`, true);
		for (const dependency of ['prometheus', 'loki', 'jaeger', 'opentelemetry_collector']) {
			const namespace = String(getPath(`components.catalog.${dependency}.namespace`) || '').trim();
			if (namespace && !form.value.namespaces[namespace]) form.value.namespaces[namespace] = {};
		}
		setComponentValue('opentelemetry_collector', 'destinations.jaeger.enabled', true);
		Message.info('已同时启用 Prometheus、Grafana、Loki 与 Jaeger，组成 Metrics / Logs / Traces 完整链路');
	}
	if (path === 'components.catalog.jaeger.enabled') {
		setPath('components.catalog.prometheus.enabled', true);
		Message.info('已同时启用 Prometheus，用于 Jaeger 自身健康指标监控');
	}
	if (path === 'components.catalog.bytebase.enabled') {
		setPath('components.catalog.mysql.enabled', true);
		Message.info('已同时启用 MySQL；Bytebase 部署后会自动登记该实例');
	}
	if (path === 'components.catalog.redisinsight.enabled') {
		setPath('components.catalog.redis.enabled', true);
		Message.info('已同时启用 Redis；RedisInsight 部署后会自动建立连接');
	}
	if (path === 'components.catalog.etcd_workbench.enabled') {
		setPath('components.etcd.enabled', true);
		const etcdNamespace = String(getPath('components.etcd.namespace') || 'platform-server').trim();
		if (etcdNamespace && !form.value.namespaces[etcdNamespace]) form.value.namespaces[etcdNamespace] = {};
		Message.info('已同时启用阶段1的 etcd；先部署阶段1，再部署阶段2的 Etcd Workbench');
	}
	if (path === 'components.catalog.clickvisual_stack.enabled') {
		const namespace = String(getPath('components.catalog.clickvisual_stack.values.namespace') || getPath('components.catalog.clickvisual_stack.namespace') || '').trim();
		if (namespace && !form.value.namespaces[namespace]) form.value.namespaces[namespace] = {};
		Message.info('已将 Fluent Bit、Kafka、ClickHouse、ClickVisual 和 MySQL 统一到一个日志专用 Namespace');
	}
	if (path === 'components.catalog.efk_stack.enabled') {
		const namespace = String(getPath('components.catalog.efk_stack.values.namespace') || getPath('components.catalog.efk_stack.namespace') || '').trim();
		if (namespace && !form.value.namespaces[namespace]) form.value.namespaces[namespace] = {};
		Message.info('已启用 Elasticsearch、Fluentd 和 Kibana，部署时会自动完成日志索引与 Kibana 视图对接');
	}
  const config = getPath(path.slice(0, -'.enabled'.length));
  const namespace = String(config?.namespace || '').trim();
  if (namespace && !form.value.namespaces[namespace]) form.value.namespaces[namespace] = {};
	if (path === 'components.catalog.loki.enabled') {
		const grafanaNamespace = String(getPath('components.catalog.prometheus.namespace') || '').trim();
		if (grafanaNamespace && !form.value.namespaces[grafanaNamespace]) form.value.namespaces[grafanaNamespace] = {};
	}
};
const componentObservedStatus = (component: ComponentConfig) => store.status?.components?.find((item) => item.key === component.key);
const componentActual = (component: ComponentConfig) => Boolean(componentObservedStatus(component)?.actual);
const componentInstalled = (key: string) => Boolean(store.status?.components?.find((item) => item.key === key)?.actual);
const clickVisualComponent = computed(() => stageTwoComponents.value.find((item) => item.key === 'clickvisual_stack') || null);
const clickVisualEnabled = computed(() => Boolean(clickVisualComponent.value && componentEnabled(clickVisualComponent.value.config_path)));
const clickVisualDeployed = computed(() => Boolean(clickVisualComponent.value && componentActual(clickVisualComponent.value)));
const clickVisualStorageComponents = [
  { key: 'kafka', name: 'Kafka', description: '日志缓冲队列', initialHint: '单个 Broker 的 PVC 容量。' },
  { key: 'clickhouse', name: 'ClickHouse', description: '日志数据与索引', initialHint: '日志检索数据盘的初始容量。' },
  { key: 'mysql', name: 'MySQL', description: 'ClickVisual 元数据', initialHint: '用户、看板和数据源配置的初始容量。' },
] as const;
const componentLifecycleLabel = (component: ComponentConfig) => {
	const enabled = componentEnabled(component.config_path); const actual = componentActual(component); const observed = componentObservedStatus(component);
	if (enabled && actual && observed?.status === 'degraded') return '运行中（更新异常）';
	if (enabled && actual) return '运行中';
	if (enabled) return '待安装';
	if (actual) return '待卸载';
	return '未安装';
};
const componentLifecycleColor = (component: ComponentConfig) => {
	const label = componentLifecycleLabel(component);
	if (label === '运行中') return 'green';
	if (label === '运行中（更新异常）') return 'orange';
	if (label === '待安装') return 'arcoblue';
	if (label === '待卸载') return 'orange';
	return 'gray';
};
const baseComponentLifecycleHint = (component: ComponentConfig) => {
	if (componentEnabled(component.config_path)) {
		return component.key === 'consul'
			? '域名转发请选择 consul-ui Service 的 443 端口和 HTTPS 协议。'
			: '域名转发请选择 etcd-web Service 的 80 端口和 HTTP 协议。';
	}
	if (!componentActual(component)) {
		return `${component.display_name} 当前未安装，保留的参数仅供下次启用，不会触发卸载操作。`;
	}
	const runtime = componentRuntimeConfig(component);
	return `执行阶段1后将卸载 ${component.display_name}；${runtime.retain_pvc_on_delete ? 'PVC 将保留' : 'PVC 将随服务删除'}，${runtime.backup.enabled ? '已有 S3 备份桶与保留策略继续保留' : '未启用备份保留策略'}。`;
};
const componentRemovalBlockers = (component: ComponentConfig) => {
	const blockers: string[] = [];
	if (component.key === 'mysql' && componentEnabled('components.catalog.bytebase.enabled')) blockers.push('先关闭 Bytebase');
	if (component.key === 'redis' && componentEnabled('components.catalog.redisinsight.enabled')) blockers.push('先关闭 RedisInsight');
	if (component.key === 'etcd' && componentEnabled('components.catalog.etcd_workbench.enabled')) blockers.push('先关闭 Etcd Workbench');
	if (component.key === 'prometheus' && componentEnabled('components.catalog.loki.enabled')) blockers.push('先关闭 Loki');
	if (component.key === 'prometheus' && Boolean(form.value.alerting?.enabled)) blockers.push('先关闭告警管理');

	const config = componentRuntimeConfig(component);
	const namespace = String(config.namespace || '');
	const serviceNames = new Set<string>([
		config.service_name, config.console_service_name, config.public_service_name,
		...(component.key === 'consul' ? ['consul-ui', 'consul-http', 'consul-server'] : []),
		...(component.key === 'etcd' ? ['etcd', 'etcd-web'] : []),
	].map((item) => String(item || '').trim()).filter(Boolean));
	const routes = (form.value.domains || []).filter((domain: Dict) => domain.enabled !== false && String(domain.namespace || '') === namespace);
	const referencedHosts = routes.filter((domain: Dict) => domainRoutes(domain).some((route: Dict) => serviceNames.has(String(route.service || '')))).map((domain: Dict) => String(domain.domain || '负载均衡地址'));
	if (referencedHosts.length) blockers.push(`先移除域名转发：${[...new Set(referencedHosts)].join('、')}`);
	if (component.key === 'higress') {
		const hosts = (form.value.domains || []).filter((domain: Dict) => domain.enabled !== false && String(domain.gateway || '') === 'higress').map((domain: Dict) => String(domain.domain || '负载均衡地址'));
		if (hosts.length) blockers.push(`先移除 Higress 路由：${[...new Set(hosts)].join('、')}`);
	}
	if (component.key === 'nginx_ingress') {
		const hosts = (form.value.domains || []).filter((domain: Dict) => domain.enabled !== false && ['nginx', 'nginx-ingress'].includes(String(domain.gateway || ''))).map((domain: Dict) => String(domain.domain || '负载均衡地址'));
		if (hosts.length) blockers.push(`先移除 NGINX Ingress 路由：${[...new Set(hosts)].join('、')}`);
	}
	return [...new Set(blockers)];
};
const resetComponentRemoval = () => { componentRemovalVisible.value = false; pendingComponentRemoval.value = null; componentRemovalAcknowledged.value = false; };
const requestComponentToggle = (component: ComponentConfig, value: boolean) => {
	if (value) { setComponent(component.config_path, true); return; }
	const blockers = componentRemovalBlockers(component);
	if (blockers.length) {
		Message.warning(`暂不能卸载 ${component.display_name}：${blockers.join('；')}`);
		return;
	}
	pendingComponentRemoval.value = component; componentRemovalAcknowledged.value = false; componentRemovalVisible.value = true;
};
const componentRemovalPhaseLabel = computed(() => pendingComponentRemoval.value && ['consul', 'etcd'].includes(pendingComponentRemoval.value.key) ? '阶段 1 · 基础资源与服务' : '阶段 2 · 组件与接入配置');
const componentRemovalActualLabel = computed(() => {
	const component = pendingComponentRemoval.value; if (!component) return '-';
	const observed = componentObservedStatus(component);
	if (observed?.actual) return `已安装${observed.detail ? `（${observed.detail}）` : ''}`;
	return observed ? '集群未发现正常运行实例' : '尚未读取集群实际状态';
});
const componentRemovalScope = computed(() => {
	const component = pendingComponentRemoval.value; if (!component) return '-';
	const config = componentRuntimeConfig(component);
	return `${String(config.namespace || 'platform-server')} / ${String(config.release_name || component.status_name || component.key)}`;
});
const componentRetentionValue = (component: ComponentConfig): boolean | null => {
	const config = componentRuntimeConfig(component);
	const candidates = component.key === 'consul' || component.key === 'etcd'
		? [config.retain_pvc_on_delete]
		: [getObjectPath(config, 'values.storage.retainOnDelete'), getObjectPath(config, 'values.persistence.retainOnDelete'), getObjectPath(config, 'values.elasticsearch.storage.retainOnDelete')];
	for (const value of candidates) if (typeof value === 'boolean') return value;
	return null;
};
const componentRemovalRetentionText = computed(() => {
	const component = pendingComponentRemoval.value; if (!component) return '-';
	const retain = componentRetentionValue(component);
	if (retain === true) return 'PVC 与持久化数据保留；重新安装前可复用或手工清理。';
	if (retain === false) return '卸载时删除该组件管理的 PVC；数据不可恢复。';
	return '该 Chart 没有声明平台统一的 PVC 保留开关，按 Helm Chart 与 Kubernetes 默认策略处理。';
});
const componentRemovalRelatedResources = computed(() => {
	const key = pendingComponentRemoval.value?.key || '';
	const known: Record<string, string> = {
		consul: 'StatefulSet、Service、UI、ACL/TLS 对象与备份 CronJob；已有 S3 备份不会被删除',
		etcd: 'StatefulSet、Service、WebUI、TLS/Secret 与备份 CronJob；已有 S3 备份不会被删除',
		loki: 'Loki Helm Release、Alloy 采集器、Grafana Loki 数据源与日志 Dashboard',
		prometheus: 'Prometheus、Grafana、Alertmanager 及平台生成的监控 Dashboard/告警路由',
		clickvisual_stack: 'Fluent Bit、Kafka、ClickHouse、ClickVisual、MySQL 及平台生成的全部凭据',
		efk_stack: 'Elasticsearch、Fluentd、Kibana 及平台生成的全部凭据',
		opentelemetry_collector: 'Collector StatefulSet、OTLP Service 与每副本独立 WAL PVC；是否保留 PVC 由当前配置决定',
		higress: 'Higress Gateway、Console 与 LoadBalancer Service',
		nginx_ingress: 'NGINX Ingress Controller 与 LoadBalancer Service',
	};
	return known[key] || 'Helm Release 中的 Deployment/StatefulSet、Service、ConfigMap、Secret 及平台生成的关联凭据';
});
const componentRemovalWarning = computed(() => {
	const component = pendingComponentRemoval.value; if (!component) return '';
	if (!componentActual(component)) return '当前集群未发现该组件；此操作主要用于取消待安装配置，Terraform 仍会清理 State 中可能遗留的关联资源。';
	return '卸载期间该组件的访问会中断。平台会在任务日志中明确列出卸载动作和数据保留结果。';
});
const confirmComponentRemoval = () => {
	const component = pendingComponentRemoval.value;
	if (!component || !componentRemovalAcknowledged.value) { Message.warning('请先确认卸载影响和数据保留策略'); return false; }
	setComponent(component.config_path, false);
	Message.warning(`已标记卸载 ${component.display_name}；请保存配置并执行${componentRemovalPhaseLabel.value}`);
	resetComponentRemoval();
	return true;
};
const catalogConfig = (key: string): Dict => form.value.components?.catalog?.[key] || {};
const selfHostedDataComponentKeys = ['mysql', 'redis', 'activemq', 'mongodb'];
const managedDatabaseConsoleKeys = ['bytebase', 'redisinsight', 'etcd_workbench'];
type ComponentAdvancedOption = { value: string; label: string };
const componentAdvancedOptions = (component: ComponentConfig): ComponentAdvancedOption[] => {
  const options: ComponentAdvancedOption[] = [{ value: 'timeout', label: '部署超时' }];
  const config = catalogConfig(component.key) || {};
  if (component.key !== 'opentelemetry_collector' && String(config.service_name || '').trim() && Number(config.service_port || 0) > 0) options.push({ value: 'access', label: '控制台访问与 TLS' });
  if (selfHostedDataComponentKeys.includes(component.key)) options.push({ value: 'resources', label: 'CPU / 内存资源' });
  if (['mysql', 'redis'].includes(component.key)) options.push({ value: 'tuning', label: '服务调优' });
  options.push({ value: 'helm_values', label: 'Helm Values 覆盖' });
  return options;
};
const componentAdvancedSelection = (component: ComponentConfig) => {
  if (!componentAdvancedSelections[component.key]) {
    const config = catalogConfig(component.key) || {};
    const selected: string[] = [];
    if (Number(config.timeout || 1200) !== 1200) selected.push('timeout');
    if (String(config.domain || '').trim() || Boolean(config.tls)) selected.push('access');
    const hasCustomValues = Object.keys(config.values || {}).length > 0 && !selfHostedDataComponentKeys.includes(component.key) && !managedDatabaseConsoleKeys.includes(component.key) && component.key !== 'loki';
    if (hasCustomValues) selected.push('helm_values');
    componentAdvancedSelections[component.key] = selected;
  }
  return componentAdvancedSelections[component.key];
};
const setComponentAdvancedSelection = (component: ComponentConfig, value: unknown) => {
  const previous = componentAdvancedSelection(component);
  const selected = Array.isArray(value) ? value.map(String) : [];
  const config = catalogConfig(component.key);
  if (previous.includes('timeout') && !selected.includes('timeout')) config.timeout = 1200;
  if (previous.includes('access') && !selected.includes('access')) { config.domain = ''; config.tls = false; }
  componentAdvancedSelections[component.key] = selected;
};
const componentAdvancedEnabled = (component: ComponentConfig, key: string) => componentAdvancedSelection(component).includes(key);
const dataServiceVersions = (key: string) => (catalogConfig(key)?.app_versions || [catalogConfig(key)?.app_version]).filter(Boolean);
const componentSelectedVersion = (component: ComponentConfig) => String([...selfHostedDataComponentKeys, ...managedDatabaseConsoleKeys].includes(component.key) ? catalogConfig(component.key)?.app_version || '' : catalogConfig(component.key)?.chart_version || '');
const componentVersionLoading = (key: string) => Boolean(componentVersionCatalogs[key]?.loading);
const componentVersionError = (key: string) => componentVersionCatalogs[key]?.error || '';
const componentVersionOptions = (component: ComponentConfig): HelmVersionOption[] => {
  const config = catalogConfig(component.key) || {};
  const source = [...selfHostedDataComponentKeys, ...managedDatabaseConsoleKeys].includes(component.key)
    ? dataServiceVersions(component.key).map((version: string) => ({ version }))
    : componentVersionCatalogs[component.key]?.options || [];
  const current = componentSelectedVersion(component); const result: HelmVersionOption[] = []; const seen = new Set<string>();
  for (const item of current ? [{ version: current }, ...source] : source) { if (!item.version || seen.has(item.version)) continue; seen.add(item.version); result.push(item); }
  return result;
};
const componentVersionHint = (component: ComponentConfig) => {
  if ([...selfHostedDataComponentKeys, ...managedDatabaseConsoleKeys].includes(component.key)) return `${componentVersionOptions(component).length} 个平台已验证版本`;
  const catalog = componentVersionCatalogs[component.key];
  if (catalog?.loading) return '正在从 Helm 仓库查询可用版本…';
  if (catalog?.error) return catalog.error;
  if (catalog?.loaded) return catalog.options.length > 1 ? `Helm 仓库返回 ${catalog.options.length} 个版本` : '当前仓库仅返回当前可用版本';
  return catalogConfig(component.key)?.repository ? '展开下拉框时从 Helm 仓库查询版本' : '平台内置组件版本';
};
const loadComponentVersions = async (component: ComponentConfig, force = false) => {
  if ([...selfHostedDataComponentKeys, ...managedDatabaseConsoleKeys].includes(component.key)) return;
  const config = catalogConfig(component.key); const current = componentVersionCatalogs[component.key];
  if (!config?.repository || !config?.chart || current?.loading || (!force && current?.loaded)) return;
  componentVersionCatalogs[component.key] = { loading: true, loaded: false, options: current?.options || [], error: '' };
  const revision = store.scopeRevision;
  try {
    const result = await store.loadHelmComponentVersions({ repository: config.repository, chart: config.chart, chart_version: config.chart_version });
    if (revision !== store.scopeRevision) return;
    componentVersionCatalogs[component.key] = { loading: false, loaded: true, options: result.versions || [], error: '' };
  } catch (error: any) {
    if (revision !== store.scopeRevision) return;
    componentVersionCatalogs[component.key] = { loading: false, loaded: true, options: current?.options || [], error: error.message };
  }
};
const componentValue = (key: string, path: string, fallback?: any) => getObjectPath(catalogConfig(key)?.values || {}, path) ?? fallback;
const setComponentValue = (key: string, path: string, value: any) => { const config = catalogConfig(key); if (!config.values || typeof config.values !== 'object') config.values = {}; setObjectPath(config.values, path, value); };
const higressNLBAllowedCIDRsText = computed(() => {
	const values = catalogConfig('higress')?.nlb?.allowed_cidrs;
	return Array.isArray(values) ? values.join('\n') : '';
});
const higressNLBSecurityGroupMode = computed(() => String(catalogConfig('higress')?.nlb?.security_group_mode || 'managed'));
const higressNLBScheme = computed(() => String(catalogConfig('higress')?.nlb?.scheme || 'internet-facing'));
const higressUsesCustomSecurityGroups = computed(() => higressNLBSecurityGroupMode.value !== 'managed');
const higressUsesManagedSecurityGroup = computed(() => higressNLBSecurityGroupMode.value !== 'custom');
const higressCanUseCustomSecurityGroups = computed(() => !existingEKSTarget.value && form.value.network?.mode === 'existing');
const setHigressNLBAllowedCIDRs = (raw: string) => {
	const config = catalogConfig('higress');
	if (!config.nlb || typeof config.nlb !== 'object' || Array.isArray(config.nlb)) config.nlb = {};
	config.nlb.allowed_cidrs = [...new Set(raw.split(/[\s,]+/).map((item) => item.trim()).filter(Boolean))];
};
const isCanonicalIPv4CIDR = (value: string) => {
	const match = value.match(/^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})\/(\d|[12]\d|3[0-2])$/);
	if (!match) return false;
	const octets = match.slice(1, 5).map(Number);
	if (octets.some((octet) => octet < 0 || octet > 255)) return false;
	const prefix = Number(match[5]);
	const address = (((octets[0] << 24) >>> 0) + (octets[1] << 16) + (octets[2] << 8) + octets[3]) >>> 0;
	const mask = prefix === 0 ? 0 : (0xffffffff << (32 - prefix)) >>> 0;
	return ((address & mask) >>> 0) === address;
};
const securityGroupLabel = (group: AWSSecurityGroupInfo) => {
  const access = [group.allows_http ? '80' : '', group.allows_https ? '443' : ''].filter(Boolean).join('/');
  const exposure = group.public_http || group.public_https ? ' · 存在全网规则' : '';
  const blocked = group.blocked_reason ? ` · 禁止：${group.blocked_reason}` : '';
  return `${group.display_name || group.name || '未命名'} · ${group.id} · ${group.vpc_id} · 入站${access || '无80/443'}${exposure}${blocked}`;
};
const higressSelectedSecurityGroups = computed(() => {
  const selected = new Set((catalogConfig('higress')?.nlb?.security_group_ids || []).map((item: unknown) => String(item)));
  return securityGroups.value.filter((group) => selected.has(group.id));
});
const higressSelectedSecurityGroupWarnings = computed(() => {
  const warnings: string[] = [];
  if (higressSelectedSecurityGroups.value.some((group) => !group.selectable)) warnings.push('已选列表包含禁止复用的安全组，请删除后再保存');
  if (higressSelectedSecurityGroups.value.some((group) => group.public_http || group.public_https)) warnings.push(higressNLBScheme.value === 'internal' ? '多个安全组权限会取并集：当前已有安全组来源为 0.0.0.0/0；内网 NLB 仍仅在其网络可达范围内生效' : '多个安全组权限会取并集：当前已有安全组包含全互联网入站规则');
  if (higressNLBSecurityGroupMode.value === 'custom' && higressSelectedSecurityGroups.value.length > 0 && !higressSelectedSecurityGroups.value.some((group) => group.allows_http || group.allows_https)) warnings.push('仅已有安全组模式下，当前选择未发现 TCP 80/443 入站规则，部署前预检将阻止执行');
  return warnings;
});
const loadSecurityGroups = async (force = false) => {
  const projectKey = store.currentProjectKey; const region = String(form.value.region || ''); const revision = store.scopeRevision;
  if (!projectKey || !region || loadingSecurityGroups.value || (!force && securityGroups.value.length)) return;
  loadingSecurityGroups.value = true; securityGroupsError.value = '';
  try {
    const query = new URLSearchParams({ region });
    if (form.value.network?.mode === 'existing' && form.value.network?.existing_vpc_id) query.set('vpc_id', String(form.value.network.existing_vpc_id));
    const response = await api<AWSSecurityGroupResponse>(`/api/projects/${encodeURIComponent(projectKey)}/aws-catalog/security-groups?${query}`);
    if (revision !== store.scopeRevision || form.value.region !== region) return;
    securityGroups.value = response.security_groups || [];
    if (!securityGroups.value.length) securityGroupsError.value = 'AWS 未返回可用安全组；也可以手动输入安全组 ID';
  } catch (error: any) {
    if (revision === store.scopeRevision) { securityGroups.value = []; securityGroupsError.value = error.message; }
  } finally { if (revision === store.scopeRevision) loadingSecurityGroups.value = false; }
};
const changeHigressNLBSecurityGroupMode = (mode: string) => {
  const config = catalogConfig('higress');
  if (!config.nlb || typeof config.nlb !== 'object' || Array.isArray(config.nlb)) config.nlb = {};
	if (mode !== 'managed' && !higressCanUseCustomSecurityGroups.value) {
		config.nlb.security_group_mode = 'managed';
		Message.warning(existingEKSTarget.value ? '接入已有 EKS 时平台不接管 NLB 安全组' : '复用已有安全组必须先在阶段1网络规划中选择“使用已有 VPC”');
		return;
	}
  config.nlb.security_group_mode = mode;
  if (!Array.isArray(config.nlb.security_group_ids)) config.nlb.security_group_ids = [];
  if (typeof config.nlb.manage_backend_security_group_rules !== 'boolean') config.nlb.manage_backend_security_group_rules = true;
  if (!['internet-facing', 'internal'].includes(String(config.nlb.scheme))) config.nlb.scheme = 'internet-facing';
  if (!Array.isArray(config.nlb.allowed_ports) || !config.nlb.allowed_ports.length) config.nlb.allowed_ports = [80, 443];
  if (mode !== 'managed') void loadSecurityGroups();
};
const setOpenTelemetryStorageClass = (value: string) => {
  setComponentValue('opentelemetry_collector', 'storage.className', value.trim());
};
const setOpenTelemetryInitialSize = (value: string) => {
  setComponentValue('opentelemetry_collector', 'storage.initialSize', value.trim());
};
const setOpenTelemetryQueueSize = (value: number) => {
  const queueSize = Math.max(1, Math.trunc(Number(value || 1)));
  setComponentValue('opentelemetry_collector', 'storage.queueSize', queueSize);
};
const setOpenTelemetryRetainOnDelete = (retain: boolean) => {
  setComponentValue('opentelemetry_collector', 'storage.retainOnDelete', retain);
};
const openTelemetryEndpoint = computed(() => `opentelemetry-collector.${String(catalogConfig('opentelemetry_collector')?.namespace || 'monitoring')}.svc.cluster.local`);
const openTelemetryElasticsearchEnabled = computed(() => Boolean(componentValue('opentelemetry_collector', 'elasticsearch.enabled', false)));
const openTelemetryElasticsearchActual = computed(() => Boolean(store.status?.components?.find((item) => item.key === 'otel_elasticsearch')?.actual));
const openTelemetryElasticsearchMode = computed(() => String(componentValue('opentelemetry_collector', 'elasticsearch.mode', 'standalone')));
const openTelemetryElasticsearchReplicas = computed(() => Number(componentValue('opentelemetry_collector', 'elasticsearch.replicas', 1)));
const openTelemetryElasticsearchVersions = computed(() => [...new Set([
	String(componentValue('opentelemetry_collector', 'elasticsearch.image.tag', '8.19.17')),
	'8.19.17', '8.18.8', '8.17.10',
].filter(Boolean))]);
const openTelemetryElasticsearchEndpoint = computed(() => `http://otel-elasticsearch.${String(catalogConfig('opentelemetry_collector')?.namespace || 'monitoring')}.svc.cluster.local:9200`);
const setOpenTelemetryElasticsearchMode = (mode: string) => {
	const selected = mode === 'cluster' ? 'cluster' : 'standalone';
	setComponentValue('opentelemetry_collector', 'elasticsearch.mode', selected);
	setComponentValue('opentelemetry_collector', 'elasticsearch.replicas', selected === 'cluster' ? Math.max(3, openTelemetryElasticsearchReplicas.value) : 1);
};
const setOpenTelemetryElasticsearchReplicas = (replicas: number) => {
	const valid = [3, 5, 7, 9].includes(Number(replicas)) ? Number(replicas) : 3;
	setComponentValue('opentelemetry_collector', 'elasticsearch.replicas', valid);
};
const setOpenTelemetryElasticsearchEnabled = (enabled: boolean) => {
	if (!enabled && jaegerStorageBackend.value === 'elasticsearch') {
		Message.warning('Jaeger 正在使用该 Elasticsearch 保存 Trace；请先将 Jaeger 存储切换为 Badger');
		return;
	}
	setComponentValue('opentelemetry_collector', 'elasticsearch.enabled', enabled);
	setComponentValue('opentelemetry_collector', 'destinations.elasticsearch.enabled', enabled);
	setComponentValue('opentelemetry_collector', 'destinations.elasticsearch.endpoint', openTelemetryElasticsearchEndpoint.value);
	if (enabled) {
		setPath('components.catalog.jaeger.enabled', true);
		setComponentValue('jaeger', 'storage.backend', 'elasticsearch');
		setComponentValue('jaeger', 'storage.elasticsearch.endpoint', openTelemetryElasticsearchEndpoint.value);
		setPath('components.catalog.jaeger.deployment_mode', 'cluster');
		setPath('components.catalog.jaeger.replicas', Math.max(3, Number(getPath('components.catalog.jaeger.replicas') || 1)));
		Message.info('已启用 OpenTelemetry 专用 Elasticsearch，并自动对接 OTel 日志与 Jaeger Trace');
	}
};
const openTelemetryEnvironmentExample = computed(() => [
  `OTEL_SERVICE_NAME=${store.currentProjectKey || 'my-service'}`,
  `OTEL_EXPORTER_OTLP_ENDPOINT=http://${openTelemetryEndpoint.value}:4318`,
  `OTEL_RESOURCE_ATTRIBUTES=deployment.environment.name=${store.currentEnvironmentKey || 'test'},service.version=1.0.0`,
  'OTEL_TRACES_EXPORTER=otlp',
  'OTEL_METRICS_EXPORTER=otlp',
  'OTEL_LOGS_EXPORTER=otlp',
].join('\n'));
const openTelemetryPreset = computed(() => {
  const replicas = Number(componentValue('opentelemetry_collector', 'replicaCount', 1));
  const size = String(componentValue('opentelemetry_collector', 'storage.initialSize', '10Gi'));
  if (replicas === 1 && size === '10Gi') return 'test';
  if (replicas === 3 && size === '20Gi') return 'production';
  return 'custom';
});
const applyOpenTelemetryPreset = (preset: string) => {
  if (!['test', 'production'].includes(preset)) return;
  const production = preset === 'production';
  setComponentValue('opentelemetry_collector', 'replicaCount', production ? 3 : 1);
  setComponentValue('opentelemetry_collector', 'storage.initialSize', production ? '20Gi' : '10Gi');
  setComponentValue('opentelemetry_collector', 'storage.queueSize', production ? 5000 : 1000);
  setComponentValue('opentelemetry_collector', 'resources.requests.cpu', production ? '500m' : '200m');
  setComponentValue('opentelemetry_collector', 'resources.requests.memory', production ? '512Mi' : '256Mi');
	setComponentValue('opentelemetry_collector', 'resources.limits.memory', production ? '2Gi' : '1Gi');
	setOpenTelemetryDestinationEnabled('jaeger', true);
	if (production) {
		if (!openTelemetryElasticsearchActual.value) {
			setComponentValue('opentelemetry_collector', 'elasticsearch.mode', 'cluster');
			setComponentValue('opentelemetry_collector', 'elasticsearch.replicas', 3);
			setComponentValue('opentelemetry_collector', 'elasticsearch.storage.initialSize', '200Gi');
			setComponentValue('opentelemetry_collector', 'elasticsearch.javaOpts', '-Xms2g -Xmx2g');
			setComponentValue('opentelemetry_collector', 'elasticsearch.resources.requests.cpu', '1');
			setComponentValue('opentelemetry_collector', 'elasticsearch.resources.requests.memory', '4Gi');
			setComponentValue('opentelemetry_collector', 'elasticsearch.resources.limits.cpu', '4');
			setComponentValue('opentelemetry_collector', 'elasticsearch.resources.limits.memory', '8Gi');
		}
		setOpenTelemetryElasticsearchEnabled(true);
	} else {
		setJaegerStorageBackend('badger');
		setComponentValue('opentelemetry_collector', 'elasticsearch.enabled', false);
		setComponentValue('opentelemetry_collector', 'destinations.elasticsearch.enabled', false);
		if (!openTelemetryElasticsearchActual.value) {
			setComponentValue('opentelemetry_collector', 'elasticsearch.mode', 'standalone');
			setComponentValue('opentelemetry_collector', 'elasticsearch.replicas', 1);
			setComponentValue('opentelemetry_collector', 'elasticsearch.storage.initialSize', '50Gi');
		}
	}
};
const setOpenTelemetryDestinationEnabled = (destination: 'jaeger' | 'tempo', enabled: boolean) => {
	if (!enabled) {
		const other = destination === 'jaeger' ? 'tempo' : 'jaeger';
		if (!Boolean(componentValue('opentelemetry_collector', `destinations.${other}.enabled`, other === 'jaeger'))) {
			Message.warning('Jaeger 和 Tempo 至少保留一个 Trace 存储出口');
			return;
		}
	}
	setComponentValue('opentelemetry_collector', `destinations.${destination}.enabled`, enabled);
	if (enabled) setPath(`components.catalog.${destination}.enabled`, true);
};
const jaegerStorageBackend = computed(() => String(componentValue('jaeger', 'storage.backend', 'badger')));
const jaegerElasticsearchEndpoint = computed(() => {
	return openTelemetryElasticsearchEndpoint.value;
});
const setJaegerStorageBackend = (backend: string) => {
	const selected = backend === 'elasticsearch' ? 'elasticsearch' : 'badger';
	setComponentValue('jaeger', 'storage.backend', selected);
	if (selected === 'elasticsearch') {
		for (const dependency of ['opentelemetry_collector', 'prometheus', 'loki', 'jaeger']) setPath(`components.catalog.${dependency}.enabled`, true);
		setComponentValue('opentelemetry_collector', 'elasticsearch.enabled', true);
		setComponentValue('opentelemetry_collector', 'destinations.elasticsearch.enabled', true);
		setComponentValue('opentelemetry_collector', 'destinations.elasticsearch.endpoint', jaegerElasticsearchEndpoint.value);
		setComponentValue('jaeger', 'storage.elasticsearch.endpoint', jaegerElasticsearchEndpoint.value);
		setPath('components.catalog.jaeger.deployment_mode', 'cluster');
		setPath('components.catalog.jaeger.replicas', Math.max(3, Number(getPath('components.catalog.jaeger.replicas') || 1)));
		Message.info('已启用 OpenTelemetry 专用 Elasticsearch 作为 Jaeger 生产 Trace 存储');
	} else {
		setPath('components.catalog.jaeger.deployment_mode', 'standalone');
		setPath('components.catalog.jaeger.replicas', 1);
	}
};
const jaegerDiskGiB = computed(() => Number(String(componentValue('jaeger', 'storage.initialSize', '20Gi')).match(/^(\d+)Gi$/)?.[1] || 20));
const setJaegerDiskGiB = (value: number) => setComponentValue('jaeger', 'storage.initialSize', `${Math.max(1, Math.trunc(value || 1))}Gi`);
const jaegerRetentionDays = computed(() => Math.max(1, Math.round(Number(String(componentValue('jaeger', 'storage.retention', '168h')).match(/^(\d+)h$/)?.[1] || 168) / 24)));
const setJaegerRetentionDays = (value: number) => setComponentValue('jaeger', 'storage.retention', `${Math.max(1, Math.trunc(value || 1)) * 24}h`);
const tempoDiskGiB = computed(() => Number(String(componentValue('tempo', 'persistence.size', '20Gi')).match(/^(\d+)Gi$/)?.[1] || 20));
const setTempoDiskGiB = (value: number) => setComponentValue('tempo', 'persistence.size', `${Math.max(1, Math.trunc(value || 1))}Gi`);
const tempoRetentionDays = computed(() => Math.max(1, Math.round(Number(String(componentValue('tempo', 'tempo.retention', '168h')).match(/^(\d+)h$/)?.[1] || 168) / 24)));
const setTempoRetentionDays = (value: number) => setComponentValue('tempo', 'tempo.retention', `${Math.max(1, Math.trunc(value || 1)) * 24}h`);
const setDataServiceVersion = (key: string, version: string) => {
	catalogConfig(key).app_version = version;
	setComponentValue(key, 'image.tag', key === 'etcd_workbench' && version === '1.1.4'
		? '1.1.4@sha256:c58de0e1b96ebdc01856c8ef87d9cd6f2113e4d8acdd32965e5d3c6cdc949b71'
		: version);
};
const setComponentSelectedVersion = (component: ComponentConfig, version: string) => { if ([...selfHostedDataComponentKeys, ...managedDatabaseConsoleKeys].includes(component.key)) setDataServiceVersion(component.key, version); else catalogConfig(component.key).chart_version = version; };
const setDataServiceUsername = (key: string, username: string) => { const value = username.trim(); catalogConfig(key).username = value; setComponentValue(key, 'auth.username', value); };
const databaseConsoleUsername = (key: string) => String(componentValue(key, key === 'bytebase' ? 'admin.email' : 'basicAuth.username', catalogConfig(key)?.username || ''));
const setDatabaseConsoleUsername = (key: string, username: string) => { const value = username.trim(); catalogConfig(key).username = value; setComponentValue(key, key === 'bytebase' ? 'admin.email' : 'basicAuth.username', value); };
const managedConsoleDescription = (key: string) => {
	if (key === 'bytebase') return '部署后自动初始化 Bytebase 管理员，并把本环境 MySQL Service 登记为受管实例。Bytebase 高级激活功能受其官方授权计划限制。';
	if (key === 'redisinsight') return '部署后预配置本环境 Redis；首次打开 RedisInsight 需要确认官方许可协议，确认后连接会自动出现。所有 Web 访问必须先通过独立 Basic Auth。';
	return 'Etcd Workbench 使用原生账号认证并持久化连接配置；部署后使用下方内网地址添加本环境 etcd 连接，旧的 etcd-web 保持不变。';
};
const etcdWorkbenchEndpoint = computed(() => {
	const namespace = String(getPath('components.etcd.namespace') || 'platform-server').trim() || 'platform-server';
	const scheme = Boolean(getPath('components.etcd.tls_enabled')) ? 'https' : 'http';
	return `${scheme}://etcd.${namespace}.svc.cluster.local:2379`;
});
const setRabbitMQUsername = (username: string) => { const value = username.trim(); catalogConfig('rabbitmq').username = value; setComponentValue('rabbitmq', 'auth.username', value); };
const lokiDiskGiB = (key: string) => { const match = String(componentValue(key, 'singleBinary.persistence.size', '20Gi')).match(/^(\d+)Gi$/); return match ? Number(match[1]) : 20; };
const setLokiDiskGiB = (key: string, value: number) => setComponentValue(key, 'singleBinary.persistence.size', `${Math.max(1, Math.trunc(value || 1))}Gi`);
const lokiRetentionDays = (key: string) => { const match = String(componentValue(key, 'loki.limits_config.retention_period', '168h')).match(/^(\d+)h$/); return match ? Math.max(1, Math.round(Number(match[1]) / 24)) : 7; };
const setLokiRetentionDays = (key: string, value: number) => { const days = Math.max(1, Math.trunc(value || 1)); setComponentValue(key, 'loki.limits_config.retention_period', `${days * 24}h`); setComponentValue(key, 'loki.compactor.retention_enabled', true); setComponentValue(key, 'loki.compactor.delete_request_store', 'filesystem'); };
const clickVisualStorageClass = computed(() => String(componentValue('clickvisual_stack', 'clickhouse.storage.className', 'gp3')));
const clickVisualNamespace = computed(() => String(componentValue('clickvisual_stack', 'namespace', catalogConfig('clickvisual_stack').namespace || 'logs-system')));
const setClickVisualNamespace = (value: string) => {
  const namespace = value.trim();
  catalogConfig('clickvisual_stack').namespace = namespace;
  setComponentValue('clickvisual_stack', 'namespace', namespace);
  if (namespace && !form.value.namespaces[namespace]) form.value.namespaces[namespace] = {};
};
const efkNamespace = computed(() => String(componentValue('efk_stack', 'namespace', catalogConfig('efk_stack').namespace || 'efk-system')));
const setEFKNamespace = (value: string) => {
  const namespace = value.trim();
  catalogConfig('efk_stack').namespace = namespace;
  setComponentValue('efk_stack', 'namespace', namespace);
  if (namespace && !form.value.namespaces[namespace]) form.value.namespaces[namespace] = {};
};
const setCatalogComponentNamespace = (component: ComponentConfig, value: string) => {
  if (component.key === 'clickvisual_stack') setClickVisualNamespace(value);
  if (component.key === 'efk_stack') setEFKNamespace(value);
};
const setClickVisualStorageClass = (value: string) => {
  const storageClass = value.trim();
  setComponentValue('clickvisual_stack', 'kafka.storage.className', storageClass);
  setComponentValue('clickvisual_stack', 'clickhouse.storage.className', storageClass);
  setComponentValue('clickvisual_stack', 'mysql.storage.className', storageClass);
};
type ClickVisualCollectionField = 'includeNamespaces' | 'excludeNamespaces' | 'includeServices' | 'excludeServices';
const logCollectionBasePath = (componentKey: string) => componentKey === 'opentelemetry_collector' ? 'agent.logs' : 'collection';
const logCollectionValues = (componentKey: string, field: ClickVisualCollectionField): string[] => {
  const value = componentValue(componentKey, `${logCollectionBasePath(componentKey)}.${field}`, []);
  return Array.isArray(value) ? value.map(String).map((item) => item.trim()).filter(Boolean) : [];
};
const clickVisualCollectionValues = (field: ClickVisualCollectionField) => logCollectionValues('clickvisual_stack', field);
const setLogCollection = (componentKey: string, field: ClickVisualCollectionField, value: unknown) => {
  const values = Array.isArray(value)
    ? [...new Set(value.map(String).map((item) => item.trim().toLowerCase()).filter(Boolean))]
    : [];
  const invalid = values.find((item) => item.length > 63 || !/^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/.test(item));
  if (invalid) {
    Message.warning(`“${invalid}”不是合法的 Kubernetes 名称，仅支持小写字母、数字和连字符`);
    return;
  }
  if (values.length > 100) {
    Message.warning('每组日志采集规则最多配置 100 项');
    return;
  }
  setComponentValue(componentKey, `${logCollectionBasePath(componentKey)}.${field}`, values);
};
const setClickVisualCollection = (field: ClickVisualCollectionField, value: unknown) => setLogCollection('clickvisual_stack', field, value);
const clickVisualCollectionNamespaceOptions = computed(() => [...new Set([
  ...namespaceRows.value.map((item) => item.name),
  ...kubernetesServiceNamespaces.value,
])].filter(Boolean).sort());
const logCollectionServiceOptions = (componentKey: string) => {
  const includedNamespaces = new Set(logCollectionValues(componentKey, 'includeNamespaces'));
  const excludedNamespaces = new Set(logCollectionValues(componentKey, 'excludeNamespaces'));
  return [...new Set(kubernetesServices.value
    .filter((service) => (!includedNamespaces.size || includedNamespaces.has(service.namespace)) && !excludedNamespaces.has(service.namespace))
    .map((service) => service.name)
    .filter(Boolean))].sort();
};
const clickVisualCollectionServiceOptions = computed(() => logCollectionServiceOptions('clickvisual_stack'));
const logCollectionConflicts = (componentKey: string) => {
  const conflicts: string[] = [];
  for (const type of ['Namespaces', 'Services'] as const) {
    const included = new Set(logCollectionValues(componentKey, `include${type}` as ClickVisualCollectionField));
    for (const name of logCollectionValues(componentKey, `exclude${type}` as ClickVisualCollectionField)) {
      if (included.has(name)) conflicts.push(`${type === 'Namespaces' ? 'Namespace' : '服务'}：${name}`);
    }
  }
  return conflicts;
};
const clickVisualCollectionConflicts = computed(() => logCollectionConflicts('clickvisual_stack'));
const logCollectionSummary = (componentKey: string) => {
  const includeNamespaces = logCollectionValues(componentKey, 'includeNamespaces');
  const excludeNamespaces = logCollectionValues(componentKey, 'excludeNamespaces');
  const includeServices = logCollectionValues(componentKey, 'includeServices');
  const excludeServices = logCollectionValues(componentKey, 'excludeServices');
  const namespaces = includeNamespaces.length ? `${includeNamespaces.length} 个指定 Namespace` : '全部 Namespace';
  const services = includeServices.length ? `${includeServices.length} 个指定服务` : '全部服务';
  const exclusions = excludeNamespaces.length + excludeServices.length;
  return `${namespaces} · ${services}${exclusions ? ` · ${exclusions} 项排除规则` : ''}`;
};
const clickVisualCollectionSummary = computed(() => logCollectionSummary('clickvisual_stack'));
const clickVisualPreset = computed(() => {
  const replicas = Number(componentValue('clickvisual_stack', 'kafka.replicas', 1));
  const kafkaSize = String(componentValue('clickvisual_stack', 'kafka.storage.size', ''));
  const clickhouseSize = String(componentValue('clickvisual_stack', 'clickhouse.storage.size', ''));
  const retention = Number(componentValue('clickvisual_stack', 'clickhouse.retentionDays', 0));
  if (replicas === 1 && kafkaSize === '50Gi' && clickhouseSize === '100Gi' && retention === 7) return 'test';
  if (replicas === 3 && kafkaSize === '200Gi' && clickhouseSize === '500Gi' && retention === 30) return 'production';
  return 'custom';
});
const applyClickVisualPreset = (preset: string) => {
  if (preset === 'test') {
    setComponentValue('clickvisual_stack', 'kafka.replicas', 1);
    setComponentValue('clickvisual_stack', 'kafka.partitions', 6);
    setComponentValue('clickvisual_stack', 'kafka.retentionHours', 24);
    setComponentValue('clickvisual_stack', 'kafka.storage.size', '50Gi');
    setComponentValue('clickvisual_stack', 'clickhouse.retentionDays', 7);
    setComponentValue('clickvisual_stack', 'clickhouse.storage.size', '100Gi');
    setComponentValue('clickvisual_stack', 'mysql.storage.size', '20Gi');
  } else if (preset === 'production') {
    setComponentValue('clickvisual_stack', 'kafka.replicas', 3);
    setComponentValue('clickvisual_stack', 'kafka.partitions', 12);
    setComponentValue('clickvisual_stack', 'kafka.retentionHours', 72);
    setComponentValue('clickvisual_stack', 'kafka.storage.size', '200Gi');
    setComponentValue('clickvisual_stack', 'clickhouse.retentionDays', 30);
    setComponentValue('clickvisual_stack', 'clickhouse.storage.size', '500Gi');
    setComponentValue('clickvisual_stack', 'mysql.storage.size', '50Gi');
  }
};
const managedStorageName = (component: string) => ({ kafka: 'Kafka', clickhouse: 'ClickHouse', mysql: 'MySQL', opentelemetry_collector: 'OpenTelemetry Collector WAL', 'otel-elasticsearch': 'OpenTelemetry Elasticsearch' }[component] || component);
const managedStorageSizeGi = (value: string) => {
  const match = String(value || '').trim().match(/^(\d+)(Gi|Ti)$/);
  if (!match) return 0;
  return Number(match[1]) * (match[2] === 'Ti' ? 1024 : 1);
};
type ClickVisualStorageComponent = typeof clickVisualStorageComponents[number]['key'];
const clickVisualStoragePath = (component: ClickVisualStorageComponent) => `${component}.storage.size`;
const clickVisualStorageDefault = (component: ClickVisualStorageComponent) => ({ kafka: '50Gi', clickhouse: '100Gi', mysql: '20Gi' }[component]);
const clickVisualConfiguredSizeGi = (component: ClickVisualStorageComponent) => managedStorageSizeGi(String(componentValue('clickvisual_stack', clickVisualStoragePath(component), clickVisualStorageDefault(component))));
const setClickVisualConfiguredSizeGi = (component: ClickVisualStorageComponent, value: number) => {
  const size = Math.max(1, Math.min(16384, Math.trunc(Number(value || 0))));
  setComponentValue('clickvisual_stack', clickVisualStoragePath(component), `${size}Gi`);
};
const clickVisualActiveStorage = (component: ClickVisualStorageComponent) => managedStorageItems.value.filter((item) => item.component === component && item.active);
const clickVisualStorageSummary = (component: ClickVisualStorageComponent) => {
  if (!managedStorageLoaded.value) return '待读取';
  const items = clickVisualActiveStorage(component);
  if (!items.length) return '未发现 PVC';
  const capacities = [...new Set(items.map((item) => item.capacity || item.requested).filter(Boolean))];
  return `${items.length} 卷 · ${capacities.join(' / ') || '未知容量'}`;
};
const clickVisualStorageDetail = (component: ClickVisualStorageComponent) => {
  if (!managedStorageLoaded.value) return '正在从 EKS 读取实际容量…';
  const items = clickVisualActiveStorage(component);
  if (!items.length) return '未发现活动 PVC，请先检查组件状态。';
  const requested = [...new Set(items.map((item) => item.requested).filter(Boolean))].join(' / ') || '—';
  const capacity = [...new Set(items.map((item) => item.capacity).filter(Boolean))].join(' / ') || '—';
  return `活动 PVC ${items.length} 个 · 请求 ${requested} · 实际 ${capacity}`;
};
const clickVisualExpandableStorage = (component: ClickVisualStorageComponent) => {
  const items = clickVisualActiveStorage(component);
  return items.length > 0 && items.every((item) => item.allow_expansion);
};
const openClickVisualStorageExpand = (component: ClickVisualStorageComponent) => {
  const target = clickVisualActiveStorage(component)[0];
  if (!target) {
    Message.warning('尚未读取到活动 PVC，请刷新实际容量后再试');
    return;
  }
  openManagedStorageResize(target, 'expand');
};
const loadManagedStorage = async () => {
  if (!store.currentProjectKey || !store.currentEnvironmentKey || loadingManagedStorage.value) return;
  const revision = store.scopeRevision;
  loadingManagedStorage.value = true;
  try {
    const response = await api<ManagedStorageReport>(`/api/projects/${encodeURIComponent(store.currentProjectKey)}/environments/${encodeURIComponent(store.currentEnvironmentKey)}/components/clickvisual-stack/storage`);
    if (revision !== store.scopeRevision) return;
    managedStorageItems.value = response.items || [];
    managedStorageLoaded.value = true;
  } catch (error: any) {
    if (revision === store.scopeRevision) {
      managedStorageItems.value = [];
      managedStorageLoaded.value = false;
      Message.error(error.message || '读取日志平台 PVC 失败');
    }
  } finally {
    if (revision === store.scopeRevision) loadingManagedStorage.value = false;
  }
};
watch(
  [activeTab, clickVisualEnabled, clickVisualDeployed, baseReady],
  ([tab, enabled, deployed, ready]) => {
    if (tab !== 'components' || !enabled || !deployed || !ready || managedStorageLoaded.value || loadingManagedStorage.value) return;
    void loadManagedStorage();
  },
  { flush: 'post', immediate: true },
);
const loadOpenTelemetryStorage = async () => {
  if (!store.currentProjectKey || !store.currentEnvironmentKey || loadingOpenTelemetryStorage.value) return;
  const revision = store.scopeRevision;
  loadingOpenTelemetryStorage.value = true;
  try {
    const response = await api<ManagedStorageReport>(`/api/projects/${encodeURIComponent(store.currentProjectKey)}/environments/${encodeURIComponent(store.currentEnvironmentKey)}/components/opentelemetry-collector/storage`);
    if (revision !== store.scopeRevision) return;
    openTelemetryStorageItems.value = response.items || [];
  } catch (error: any) {
    if (revision === store.scopeRevision) {
      openTelemetryStorageItems.value = [];
      Message.error(error.message || '读取 Collector WAL PVC 失败');
    }
  } finally {
    if (revision === store.scopeRevision) loadingOpenTelemetryStorage.value = false;
  }
};
const resetManagedStorageResize = () => {
  managedStorageResizeVisible.value = false;
  managedStorageResizeTarget.value = null;
  managedStorageResizeOperation.value = 'expand';
  managedStorageTargetGi.value = 1;
  managedStorageSafetyPercent.value = 30;
  managedStorageResizeAcknowledged.value = false;
  submittingManagedStorageResize.value = false;
};
const openManagedStorageResize = (storage: ManagedStorage, operation: 'expand' | 'shrink') => {
  const current = managedStorageSizeGi(storage.requested || storage.capacity);
  managedStorageResizeTarget.value = storage;
  managedStorageResizeOperation.value = operation;
  managedStorageTargetGi.value = operation === 'expand' ? Math.max(current + 10, Math.ceil(current * 1.25)) : Math.max(1, Math.floor(current * 0.75));
  managedStorageSafetyPercent.value = Number(componentValue('clickvisual_stack', 'storage.shrinkSafetyPercent', 30));
  managedStorageResizeAcknowledged.value = false;
  managedStorageResizeVisible.value = true;
};
const submitManagedStorageResize = async () => {
  const target = managedStorageResizeTarget.value;
  if (!target) return false;
  const current = managedStorageSizeGi(target.requested || target.capacity);
  const targetGi = Math.trunc(Number(managedStorageTargetGi.value || 0));
  if (managedStorageResizeOperation.value === 'expand' && targetGi <= current) { Message.warning(`扩容目标必须大于当前 ${current}Gi`); return false; }
  if (managedStorageResizeOperation.value === 'shrink' && targetGi >= current) { Message.warning(`缩容目标必须小于当前 ${current}Gi`); return false; }
  if (managedStorageResizeOperation.value === 'shrink' && !managedStorageResizeAcknowledged.value) { Message.warning('请先确认缩容停服与旧盘保留说明'); return false; }
  submittingManagedStorageResize.value = true;
	  try {
	    const operation = managedStorageResizeOperation.value;
	    const openTelemetryStorage = target.component === 'opentelemetry_collector' || target.component === 'otel-elasticsearch';
	    const storageEndpoint = openTelemetryStorage ? 'opentelemetry-collector/storage/expand' : 'clickvisual-stack/storage/resize';
	    const job = await api<Job>(`/api/projects/${encodeURIComponent(store.currentProjectKey)}/environments/${encodeURIComponent(store.currentEnvironmentKey)}/components/${storageEndpoint}`, {
      method: 'POST',
      body: JSON.stringify({
        component: target.component,
        operation,
        target_size_gib: targetGi,
        safety_percent: Math.trunc(Number(managedStorageSafetyPercent.value || 30)),
        confirm: `${store.currentProjectKey}/${store.currentEnvironmentKey}:${target.component}:${operation}`,
      }),
    });
    resetManagedStorageResize();
    Message.success(operation === 'expand' ? '在线扩容任务已创建' : '安全迁移缩容任务已创建');
    await router.push({ name: 'jobs', query: { job: job.id } });
    return true;
  } catch (error: any) {
    Message.error(error.message || '创建存储任务失败');
    return false;
  } finally {
    submittingManagedStorageResize.value = false;
  }
};

type ComponentParameterRow = { path: string; type: string; value: any };
const flattenComponentValues = (value: any, prefix = '', result: ComponentParameterRow[] = []): ComponentParameterRow[] => {
  if (Array.isArray(value)) { if (prefix) result.push({ path: prefix, type: 'json', value }); return result; }
  if (value && typeof value === 'object') { const entries = Object.entries(value); if (!entries.length && prefix) result.push({ path: prefix, type: 'json', value }); else for (const [key, child] of entries) flattenComponentValues(child, prefix ? `${prefix}.${key}` : key, result); return result; }
  if (prefix) result.push({ path: prefix, type: value === null ? 'string' : typeof value, value });
  return result;
};
const parsedComponentValues = computed<Dict>(() => { try { const parsed = parse(componentValuesYAML.value || '{}'); return parsed && typeof parsed === 'object' && !Array.isArray(parsed) ? parsed : {}; } catch { return {}; } });
const componentParameterRows = computed(() => flattenComponentValues(parsedComponentValues.value));
const componentDefaultParameterRows = computed(() => {
  const selected = new Set(componentParameterRows.value.map((item) => item.path));
  return flattenComponentValues(componentValuesDefaults.value).filter((item) => !selected.has(item.path));
});
const componentValuesCanInspect = computed(() => {
  const config = catalogConfig(componentValuesKey.value);
  return Boolean(config?.repository && config?.chart);
});
const componentValuesSummary = (key: string) => `${flattenComponentValues(catalogConfig(key)?.values || {}).length} 个参数`;
const updateComponentParameter = (path: string, value: any) => { const values = JSON.parse(JSON.stringify(parsedComponentValues.value)); setObjectPath(values, path, value); componentValuesYAML.value = stringify(values); };
const updateComponentJSONParameter = (path: string, value: string) => { try { updateComponentParameter(path, JSON.parse(value)); } catch { Message.warning('数组或对象请输入合法 JSON'); } };
const parameterPreview = (value: any) => { const text = typeof value === 'string' ? value : JSON.stringify(value); return String(text ?? '').length > 48 ? `${String(text).slice(0, 48)}…` : String(text ?? ''); };
const deleteObjectPath = (source: Dict, path: string) => {
  const keys = path.split('.'); const parents: Array<{ value: Dict; key: string }> = []; let target: any = source;
  for (const key of keys.slice(0, -1)) { if (!target || typeof target !== 'object' || Array.isArray(target)) return; parents.push({ value: target, key }); target = target[key]; }
  if (!target || typeof target !== 'object' || Array.isArray(target)) return;
  delete target[keys[keys.length - 1]];
  for (let index = parents.length - 1; index >= 0; index -= 1) { const parent = parents[index]; const child = parent.value[parent.key]; if (child && typeof child === 'object' && !Array.isArray(child) && !Object.keys(child).length) delete parent.value[parent.key]; else break; }
};
const removeComponentParameter = (path: string) => { const values = JSON.parse(JSON.stringify(parsedComponentValues.value)); deleteObjectPath(values, path); componentValuesYAML.value = stringify(values); componentValuesCandidatePaths.value = componentValuesCandidatePaths.value.filter((item) => item !== path); };
const addSelectedComponentParameters = () => {
  const values = JSON.parse(JSON.stringify(parsedComponentValues.value));
  for (const path of componentValuesCandidatePaths.value) { const value = getObjectPath(componentValuesDefaults.value, path); if (value !== undefined) setObjectPath(values, path, JSON.parse(JSON.stringify(value))); }
  const count = componentValuesCandidatePaths.value.length; componentValuesYAML.value = stringify(values); componentValuesCandidatePaths.value = [];
  componentValuesMessage.value = `已添加 ${count} 个参数，可继续修改覆盖值`; componentValuesError.value = false;
};
const openComponentValues = (component: ComponentConfig) => { componentValuesKey.value = component.key; componentValuesName.value = component.display_name; componentValuesTab.value = 'form'; componentValuesMessage.value = ''; componentValuesError.value = false; componentValuesDefaults.value = {}; componentValuesCandidatePaths.value = []; const values = catalogConfig(component.key)?.values || {}; componentValuesYAML.value = stringify(values); componentValuesOriginal.value = componentValuesYAML.value; componentValuesVisible.value = true; };
const resetComponentValues = () => { componentValuesYAML.value = componentValuesOriginal.value; componentValuesCandidatePaths.value = []; componentValuesMessage.value = '已恢复为打开窗口时的参数'; componentValuesError.value = false; };
const loadComponentChartDefaults = async () => {
  const config = catalogConfig(componentValuesKey.value); if (!config || componentValuesLoading.value) return;
  const revision = store.scopeRevision; const componentKey = componentValuesKey.value;
  componentValuesLoading.value = true; componentValuesMessage.value = ''; componentValuesError.value = false;
  try {
    const result = await store.inspectHelmComponent({ repository: config.repository, chart: config.chart, chart_version: config.chart_version });
    if (revision !== store.scopeRevision || componentValuesKey.value !== componentKey) return;
    componentValuesDefaults.value = result.values || {}; componentValuesCandidatePaths.value = [];
    const filtered = result.filtered_sensitive_paths?.length ? `，已自动过滤 ${result.filtered_sensitive_paths.length} 个敏感字段` : '';
    componentValuesMessage.value = `查询到 ${componentDefaultParameterRows.value.length} 个可选参数${filtered}；请选择后再添加`;
  }
  catch (error: any) { if (revision === store.scopeRevision && componentValuesKey.value === componentKey) { componentValuesError.value = true; componentValuesMessage.value = error.message; } }
  finally { if (revision === store.scopeRevision && componentValuesKey.value === componentKey) componentValuesLoading.value = false; }
};
const saveComponentValues = () => {
  if (new TextEncoder().encode(componentValuesYAML.value).length > 1 << 20) { Message.error('Helm Values 不能超过 1 MiB'); componentValuesTab.value = 'yaml'; return false; }
  let values: any; try { values = parse(componentValuesYAML.value || '{}'); } catch (error: any) { Message.error(`Values YAML 格式不合法：${error.message}`); componentValuesTab.value = 'yaml'; return false; }
  if (!values || typeof values !== 'object' || Array.isArray(values)) { Message.error('Helm Values 顶层必须是对象'); componentValuesTab.value = 'yaml'; return false; }
  catalogConfig(componentValuesKey.value).values = JSON.parse(JSON.stringify(values));
  Message.success(`${componentValuesName.value} 参数已应用，请保存部署配置`);
  return true;
};
const addNamespace = () => {
  const name = newNamespace.value.trim().toLowerCase();
  if (!/^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/.test(name) || name.length > 63) {
    Message.warning('Namespace名称不符合Kubernetes RFC 1123');
    return false;
  }
  if (form.value.namespaces[name]) {
    Message.warning('Namespace已存在');
    return false;
  }
  form.value.namespaces[name] = {};
  newNamespace.value = '';
  return true;
};
const validateDataServiceCredentialInputs = () => {
  for (const service of ['rds', 'aurora']) {
    const config = form.value.data_services?.[service];
    if (!config?.enabled || config.credential_management !== 'self-managed') continue;
    const username = String(config.master_username || '').trim();
    const password = String(dataServicePasswords[service] || '');
    const existing = dataServiceCredentialInfos[service];
    if (!username) throw new Error(`${service.toUpperCase()} 管理员用户名不能为空`);
    if (!password && (!existing?.configured || existing.username !== username)) {
      throw new Error(`${service.toUpperCase()} 选择了自我管理凭证，请输入管理员密码`);
    }
  }
};
const persistDataServiceCredentials = async () => {
  const projectKey = store.currentProjectKey; const environmentKey = store.currentEnvironmentKey; const revision = store.scopeRevision;
  for (const service of ['rds', 'aurora']) {
    const config = form.value.data_services?.[service];
    const password = String(dataServicePasswords[service] || '');
    if (!config?.enabled || config.credential_management !== 'self-managed' || !password) {
      if (config?.credential_management !== 'self-managed') dataServicePasswords[service] = '';
      continue;
    }
    const saved = await api<DataServiceCredentialInfo>(`/api/projects/${encodeURIComponent(projectKey)}/environments/${encodeURIComponent(environmentKey)}/data-service-credentials/${service}`, {
      method: 'PUT', body: JSON.stringify({ username: String(config.master_username || '').trim(), password }),
    });
    dataServicePasswords[service] = '';
    if (revision === store.scopeRevision) dataServiceCredentialInfos[service] = saved;
  }
};
const validateEKSNodeGroups = () => {
	const roleCounts: Record<string, number> = {};
	for (const [name, group] of Object.entries(form.value.eks?.node_groups || {}) as Array<[string, Dict]>) {
		const instanceTypes = [...new Set((Array.isArray(group.instance_types) ? group.instance_types : [])
			.map((value: unknown) => String(value).trim())
			.filter(Boolean))];
		if (instanceTypes.length < 1 || instanceTypes.length > 20) {
			throw new Error(`EKS 节点组「${name}」必须选择 1 到 20 种实例类型`);
		}
		group.instance_types = instanceTypes;
		const role = nodeRole(group);
		roleCounts[role] = (roleCounts[role] || 0) + 1;
	}
	if (form.value.eks?.workload_scheduling?.enabled && (!roleCounts.application || !roleCounts.platform)) {
		throw new Error('新环境至少需要一个业务服务节点组和一个运维组件节点组');
	}
};
const validateHigressNLBInputs = () => {
	const higress = catalogConfig('higress');
	if (!higress?.enabled) return;
	if (!higress.nlb || typeof higress.nlb !== 'object' || Array.isArray(higress.nlb)) throw new Error('Higress NLB 配置不完整，请重新选择入口安全设置');
	const mode = String(higress.nlb.security_group_mode || 'managed');
	if (!['managed', 'custom', 'managed_plus_custom'].includes(mode)) throw new Error('Higress NLB 前端安全组模式无效');
	if (mode !== 'managed' && !higressCanUseCustomSecurityGroups.value) throw new Error('复用已有安全组只适用于“使用已有 VPC”的平台托管 EKS');
	const ids = [...new Set<string>((Array.isArray(higress.nlb.security_group_ids) ? higress.nlb.security_group_ids : []).map((item: unknown) => String(item).trim().toLowerCase()).filter(Boolean))];
	if (ids.length > 4) throw new Error('Higress NLB 最多选择 4 个已有安全组；平台守护安全组会作为第 5 个自动绑定');
	if (ids.some((id) => !/^sg-[0-9a-f]{8,17}$/.test(id))) throw new Error('Higress NLB 安全组 ID 格式不正确，应为 sg-xxxxxxxx');
	if (mode !== 'managed' && ids.length === 0) throw new Error('当前安全组模式必须至少选择一个已有安全组');
	if (securityGroups.value.some((group) => ids.includes(group.id) && !group.selectable)) throw new Error('已选择的安全组包含默认、EKS 集群或平台守护安全组，请移除后保存');
	higress.nlb.security_group_ids = ids;
	const ports = [...new Set((Array.isArray(higress.nlb.allowed_ports) ? higress.nlb.allowed_ports : []).map((item: unknown) => Number(item)))];
	if (!ports.length || ports.some((port) => port !== 80 && port !== 443)) throw new Error('Higress NLB 入口端口只能选择 80、443，且至少选择一个');
	higress.nlb.allowed_ports = ports;
	if (!['internet-facing', 'internal'].includes(String(higress.nlb.scheme))) throw new Error('Higress NLB 网络类型必须选择公网或内网');
	const cidrs = Array.isArray(higress.nlb.allowed_cidrs) ? [...new Set<string>(higress.nlb.allowed_cidrs.map((item: unknown) => String(item).trim()).filter(Boolean))] : [];
	if (mode !== 'custom' && cidrs.length === 0) throw new Error('平台管理安全组时必须至少填写一个 IPv4 来源 CIDR');
	if (cidrs.length > 30) throw new Error('Higress NLB 来源 IPv4 CIDR 最多填写 30 条');
	const invalidCIDR = cidrs.find((cidr) => !isCanonicalIPv4CIDR(cidr));
	if (invalidCIDR) throw new Error(`Higress NLB 来源必须是规范 IPv4 CIDR；请修正 ${invalidCIDR}（例如单个地址使用 /32）`);
	higress.nlb.allowed_cidrs = cidrs;
};
const persistEnvironmentDraft = async () => {
	form.value.project = store.currentProjectKey;
	form.value.environment = store.currentEnvironmentKey;
	validateEKSNodeGroups();
	validateHigressNLBInputs();
	validateDataServiceCredentialInputs();
	await store.saveEnvironment(form.value);
	await persistDataServiceCredentials();
	tlsMaterialChanged.value = false;
};
const refreshAWSConfiguration = async () => {
  try {
    await store.loadAWSConfiguration();
	const unavailable = Number(store.resources?.cloud_sync?.unavailable_resources || 0);
	if (unavailable > 0) {
	  Message.warning(`AWS 实际配置已重新读取，但仍有 ${unavailable} 个资源读取失败；请查看页面上的具体原因`);
	  return;
	}
    Message.success('已重新读取 AWS 实际资源与配置参数');
  } catch (error: any) {
    Message.error(error.message);
  }
};
const syncAWSConfiguration = async () => {
  if (dirty.value) {
    Message.warning('当前页面有未保存修改，请先保存或刷新页面，避免被 AWS 实际值覆盖');
    return false;
  }
  syncingAWSConfiguration.value = true;
  try {
    await store.syncEnvironmentAWSConfiguration();
    Message.success('平台部署配置已采用 AWS 当前实际参数，未修改云上资源');
    return true;
  } catch (error: any) {
    Message.error(error.message);
    return false;
  } finally {
    syncingAWSConfiguration.value = false;
  }
};
const save = async () => {
	  saving.value = true;
	  const savedTab = activeTab.value;
	  const savedTLSRequiresComponentPhase = tlsRequiresComponentPhase.value;
		  try {
		    await persistEnvironmentDraft();
    if (savedTab === 'tls') {
      if (!store.canDeploy) { Message.success('TLS 配置已保存；当前用户没有项目部署权限，未自动应用到 EKS'); return; }
      if (!awsCredentialReady.value) { Message.warning('TLS 配置已保存；当前项目未绑定 AWS 凭据，暂时无法自动应用到 EKS'); return; }
      if (!baseReady.value) { Message.warning(existingEKSTarget.value ? 'TLS 配置已保存；已有 EKS 接入检查未通过，暂时无法自动应用' : 'TLS 配置已保存；阶段1集群尚未就绪，暂时无法自动应用'); return; }
      jobSubmitting.value = true;
      try {
        const action: JobAction = savedTLSRequiresComponentPhase ? 'platform' : 'tls';
        const job = await store.createJob(action, scopeName.value);
        Message.success(savedTLSRequiresComponentPhase ? 'TLS 配置已保存，cert-manager 阶段2任务已自动创建' : 'TLS 配置已保存，TLS Secret 应用任务已自动创建');
        await router.push({ name: 'jobs', query: { job: job.id } });
      } catch (error: any) {
        Message.warning(`TLS 配置已保存，但自动应用任务未启动：${error.message}`);
      } finally {
        jobSubmitting.value = false;
      }
      return;
    }
    Message.success(store.canDeploy && awsCredentialReady.value
		? `部署配置已保存，可以${currentStage.value === 1 ? (phaseOneDeployed.value ? '更新部署【阶段一】' : '开始部署【阶段一】') : (phaseTwoDeployed.value ? '更新部署【阶段二】' : '开始部署【阶段二】')}`
		: '部署配置已保存');
  } catch (error:any) { Message.error(error.message); }
  finally { saving.value = false; }
};
const startPhaseOne = () => {
	const label = phaseOneDeployed.value ? '更新部署【阶段一】' : '开始部署【阶段一】';
	Modal.confirm({
		title: label,
		content: `将对 ${scopeName.value} 的 VPC、EKS、云中间件与云数据库、ECR 和基础服务执行 Terraform 计划，并在同一个任务中应用该计划。已存在资源只做差异更新。`,
		okText: label,
		onOk: async () => {
			jobSubmitting.value = true;
			try {
				const job = await store.createJob('deploy', scopeName.value);
				Message.success(`${label}任务已创建，执行日志会先展示 Terraform 计划`);
				await router.push({ name: 'jobs', query: { job: job.id } });
			} catch (error: any) { Message.error(error.message); }
			finally { jobSubmitting.value = false; }
		},
	});
};
const startPhaseTwo = () => {
	if (!baseReady.value) { Message.warning(existingEKSTarget.value ? '已有 EKS 接入检查未通过，请刷新状态并检查集群权限' : '请先完成阶段 1，等待 EKS 状态变为 ACTIVE'); return; }
	const accessOnly = accessOnlyDeployment.value;
	const label = accessOnly ? '更新接入配置' : (phaseTwoDeployed.value ? '更新部署【阶段二】' : '开始部署【阶段二】');
	const higressEnabled = Boolean(catalogConfig('higress')?.enabled);
	const nlbSummary = higressEnabled && !accessOnly
		? `\nHigress NLB：${higressNLBScheme.value === 'internal' ? '内网' : '公网'}，安全组模式 ${higressNLBSecurityGroupMode.value}；任务会在 Terraform 前校验 Load Balancer Controller、VPC 归属和安全组用途。`
		: '';
  Modal.confirm({
		title: label,
		content: accessOnly
			? `只更新 ${scopeName.value} 的域名、TCP 转发、TLS 与告警接入配置；不会安装、升级或重建 RabbitMQ、Loki、Jenkins 等组件。`
			: `将应用已保存的 Namespace、基础服务、可选组件、TLS证书、域名、告警和日志配置到 ${scopeName.value}${existingEKSTarget.value ? `（集群 ${form.value.deployment_target.cluster_name}）` : ''}。${nlbSummary}`,
		okText: label,
    onOk: async () => {
      jobSubmitting.value = true;
      try { const job = await store.createJob(accessOnly ? 'access' : 'platform', scopeName.value); Message.success(`${label}任务已创建`); await router.push({ name: 'jobs', query: { job: job.id } }); }
      catch (error:any) { Message.error(error.message); }
      finally { jobSubmitting.value = false; }
    },
  });
};
const startCurrentPhase = async () => {
	if (environmentBusy.value) {
		Message.warning('当前环境已有任务在执行，请等待任务结束后再更新部署');
		return;
	}
	const scopeRevision = store.scopeRevision;
	const stage = currentStage.value;
	if (dirty.value) {
		if (!store.canConfigure) {
			Message.warning('当前配置已修改，但用户没有配置修改权限');
			return;
		}
		saving.value = true;
		try {
			await persistEnvironmentDraft();
			if (scopeRevision !== store.scopeRevision) {
				Message.warning('项目或环境已切换，已取消创建原环境的部署任务');
				return;
			}
			Message.success('当前页面的域名、组件与其他配置已自动保存');
		} catch (error: any) {
			Message.error(`配置保存失败，未创建部署任务：${error.message}`);
			return;
		} finally {
			saving.value = false;
		}
	}
	if (scopeRevision !== store.scopeRevision) return;
	if (stage === 1) startPhaseOne(); else startPhaseTwo();
};
function remapZones(region: string) { const old = [...(form.value.network.availability_zones || [])]; const next = ['a','b','c'].map((suffix) => `${region}${suffix}`); const publicCIDRs = old.map((zone) => form.value.network.public_subnets[zone]); const privateCIDRs = old.map((zone) => form.value.network.private_subnets[zone]); form.value.network.availability_zones = next; form.value.network.public_subnets = Object.fromEntries(next.map((zone,index) => [zone, publicCIDRs[index] || `10.40.${index * 16}.0/20`])); form.value.network.private_subnets = Object.fromEntries(next.map((zone,index) => [zone, privateCIDRs[index] || `10.40.${64 + index * 16}.0/20`])); form.value.network.workload_subnet_zones = [...next]; form.value.network.data_subnet_zones = [...next]; for (const group of Object.values(form.value.eks.node_groups || {}) as Dict[]) group.availability_zones = next; }
const changeRegion = (value: unknown) => { securityGroups.value = []; securityGroupsError.value = ''; if (!existingEKSTarget.value && form.value.network.mode !== 'existing') remapZones(String(value)); if (form.value.network.mode === 'existing') { form.value.network.existing_vpc_id = ''; form.value.network.existing_vpc_cidr = ''; form.value.network.existing_workload_subnet_ids = []; form.value.network.existing_data_subnet_ids = []; form.value.network.availability_zones = []; vpcs.value = []; vpcsError.value = ''; void loadVPCs(true); } eksVersions.value = []; Object.keys(cloudCatalogs).forEach((key) => delete cloudCatalogs[key]); if (activeTab.value === 'eks') void loadEKSVersions(); }; const resetRegionNetwork = () => { const region = form.value.region; form.value.network.availability_zones = []; form.value.network.public_subnets = {}; form.value.network.private_subnets = {}; remapZones(region); Message.success('已按3个AZ重建网络规划'); };
const nodeRole = (group: Dict) => String(group.labels?.['workload-class'] || 'general');
const nodeRoleName = (role: string) => nodeRoleOptions.find((item) => item.value === role)?.label || '通用节点组';
const nodeSchedulingHint = (name: string, group: Dict) => {
	const role = nodeRole(group);
	if (role === 'gateway') return `Higress / Ingress 网关 → workload-class=gateway → ${name}`;
	if (role === 'application') return `业务 Deployment → workload-class=application → ${name}`;
	if (role === 'platform') return `阶段2运维组件 → workload-class=platform → ${name}`;
	if (role === 'stateful') return `指定的有状态服务 → workload-class=stateful → ${name}`;
	return '不自动绑定，由 Kubernetes 通用调度';
};
const updateNodeRole = (name: string, group: Dict, value: unknown) => {
	if (nodeGroupFieldLocked(name)) return;
	const role = String(value);
	if (!nodeRoleOptions.some((item) => item.value === role)) return;
	group.labels ||= {};
	group.labels['workload-class'] = role;
	group.taints = role === 'application' ? [{ key: 'workload-class', value: 'application', effect: 'NO_SCHEDULE' }] : [];
};
const nextNodeRole = () => {
	const roles = new Set(nodeGroups.value.map(([, group]) => nodeRole(group)));
	if (!roles.has('gateway')) return 'gateway';
	if (!roles.has('application')) return 'application';
	if (!roles.has('platform')) return 'platform';
	return 'general';
};
const addNodeGroup = () => { const name = newNodeGroup.value.trim(); if (!/^[a-z][a-z0-9-]{0,62}$/.test(name) || form.value.eks.node_groups[name]) { Message.warning('节点组名称不合法或已存在'); return false; } const role = nextNodeRole(); const poolNames: Record<string,string> = { gateway: 'ingress-gateway', application: 'business-workload', platform: 'platform-ops' }; form.value.eks.node_groups[name] = { availability_zones: [...form.value.network.availability_zones], instance_types: ['m7i.large'], capacity_type: 'ON_DEMAND', subnet_type: 'private', min_size: 1, desired_size: 1, max_size: 3, disk_size: 80, capacity_deferred: false, labels: { 'workload-class': role, 'ops-deploy.io/pool': poolNames[role] || name }, taints: role === 'application' ? [{ key: 'workload-class', value: 'application', effect: 'NO_SCHEDULE' }] : [] }; newNodeGroup.value = ''; return true; };
const removeNodeGroup = (name: string) => {
	if (nodeGroupFieldLocked(name)) { Message.warning(`已有节点组「${name}」受保护，不能删除`); return; }
	delete form.value.eks.node_groups[name];
};
const updateNodeGroupInstanceTypes = (name: string, group: Dict, values: unknown) => {
	if (nodeGroupFieldLocked(name)) return;
	const instanceTypes = [...new Set((Array.isArray(values) ? values : [])
		.map((value: unknown) => String(value).trim())
		.filter(Boolean))];
	if (instanceTypes.length === 0) {
		Message.warning(`节点组「${name}」至少需要保留一种实例类型`);
		return;
	}
	if (instanceTypes.length > 20) {
		Message.warning(`节点组「${name}」最多只能选择 20 种实例类型`);
		return;
	}
	group.instance_types = instanceTypes;
};
const handleTabChange = (key: string | number) => { activeTab.value = String(key); if (key === 'eks' && !eksVersions.value.length) void loadEKSVersions(); if (key === 'network' && form.value.network.mode === 'existing') void loadVPCs(); };
async function loadEKSVersions() {
  if (!store.currentProjectKey || !form.value.region || loadingEKSVersions.value) return;
  const projectKey = store.currentProjectKey; const region = String(form.value.region); const revision = store.scopeRevision;
  loadingEKSVersions.value = true; eksVersionsError.value = '';
  try {
    const response = await api<EKSVersionResponse>(`/api/projects/${encodeURIComponent(projectKey)}/aws-catalog/eks-versions?region=${encodeURIComponent(region)}`);
    if (revision === store.scopeRevision && form.value.region === region) eksVersions.value = response.versions;
  }
  catch (error:any) { if (revision === store.scopeRevision) { eksVersionsError.value = error.message; Message.warning(error.message); } }
  finally { if (revision === store.scopeRevision) loadingEKSVersions.value = false; }
}
const eksVersionStatus = (version: EKSVersionInfo) => ({ STANDARD_SUPPORT: '标准支持', EXTENDED_SUPPORT: '延长支持', UNSUPPORTED: '停止支持' }[version.status] || version.status);
const openInstanceCatalog = (name: string, group: Dict) => { instanceCatalogGroup.value = name; const current = String(group.instance_types?.[0] || 'm7i'); instanceCatalogQuery.value = current.split('.')[0] || 'm7i'; instanceTypes.value = []; instanceCatalogError.value = ''; instanceCatalogVisible.value = true; void loadInstanceTypes(); };
async function loadInstanceTypes() {
  const query = instanceCatalogQuery.value.trim().toLowerCase();
  if (!/^[a-z0-9][a-z0-9.-]{0,31}$/.test(query)) { instanceCatalogError.value = '请输入合法实例族或实例规格，例如 m7i、c7g、r7i.2xlarge'; return; }
  const projectKey = store.currentProjectKey; const region = String(form.value.region); const revision = store.scopeRevision;
  loadingInstanceTypes.value = true; instanceCatalogError.value = '';
  try {
    const response = await api<EC2InstanceTypeResponse>(`/api/projects/${encodeURIComponent(projectKey)}/aws-catalog/instance-types?region=${encodeURIComponent(region)}&query=${encodeURIComponent(query)}`);
    if (revision !== store.scopeRevision) return;
    instanceTypes.value = response.instance_types;
    if (!response.instance_types.length) instanceCatalogError.value = `AWS 在 ${region} 未返回匹配 ${query} 的实例规格`;
  }
  catch (error:any) { if (revision === store.scopeRevision) { instanceTypes.value = []; instanceCatalogError.value = error.message; } }
  finally { if (revision === store.scopeRevision) loadingInstanceTypes.value = false; }
}
const instanceSelected = (name: string) => Boolean(form.value.eks?.node_groups?.[instanceCatalogGroup.value]?.instance_types?.includes(name));
const toggleInstanceType = (name: string) => { if (nodeGroupFieldLocked(instanceCatalogGroup.value)) return; const group = form.value.eks?.node_groups?.[instanceCatalogGroup.value]; if (!group) return; if (!Array.isArray(group.instance_types)) group.instance_types = []; const index = group.instance_types.indexOf(name); if (index >= 0) { if (group.instance_types.length === 1) { Message.warning('节点组至少保留一种实例类型'); return; } group.instance_types.splice(index, 1); } else group.instance_types.push(name); };
const formatMemory = (mib: number) => mib >= 1024 ? `${Number((mib / 1024).toFixed(2))} GiB` : `${mib} MiB`;
const tlsMaterialInfo = (key: string) => store.tlsCertificates.find((item) => item.key === key);
const currentTLSMaterial = computed(() => tlsMaterialInfo(String(certificateDraft.key || '')));
const certificateModeName = (mode: string) => ({ 'cert-manager': 'cert-manager签发', 'existing-secret': '已有Secret', 'uploaded-pem': '粘贴证书' }[mode] || mode);
const certificateModeColor = (mode: string) => ({ 'cert-manager': 'arcoblue', 'existing-secret': 'purple', 'uploaded-pem': 'green' }[mode] || 'gray');
const certificateDomains = (certificate: Dict) => {
  const domains = certificate.mode === 'uploaded-pem' ? tlsMaterialInfo(certificate.key)?.dns_names : certificate.domains;
  return (domains || []).join('、') || (certificate.mode === 'existing-secret' ? '由已有Secret提供' : '证书未包含DNS名称');
};
const formatCertificateTime = (value?: string) => value ? new Date(value).toLocaleString('zh-CN') : '—';
const clearCertificateDraft = () => {
  certificateDraft.certificate_pem = '';
  certificateDraft.private_key_pem = '';
  Object.keys(certificateDraft).forEach((key) => delete certificateDraft[key]);
  certificateIndex.value = -1;
};
const openCertificate = (index = -1) => {
  clearCertificateDraft();
  certificateIndex.value = index;
  Object.assign(certificateDraft, index >= 0 ? JSON.parse(JSON.stringify(form.value.tls.certificates[index])) : { enabled: true, key: '', mode: 'cert-manager', domains: [], namespace: 'platform-server', tls_secret_name: '', certificate_name: '', issuer_name: 'letsencrypt-prod', issuer_kind: 'ClusterIssuer' });
  certificateDraft.certificate_pem = '';
  certificateDraft.private_key_pem = '';
  certificateVisible.value = true;
};
const saveCertificate = async () => {
  const scopeRevision = store.scopeRevision;
  const key = String(certificateDraft.key || '').trim();
  const secretName = String(certificateDraft.tls_secret_name || '').trim();
  const namespace = String(certificateDraft.namespace || '').trim();
  if (!/^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/.test(key) || !/^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/.test(secretName) || !/^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/.test(namespace)) { Message.warning('证书标识、Namespace 或 TLS Secret 名称不合法'); return false; }
  if (certificateDraft.mode === 'cert-manager' && !(certificateDraft.domains || []).length) { Message.warning('cert-manager模式至少填写一个证书域名'); return false; }
  if (form.value.tls.certificates.some((item: Dict, index: number) => item.key === key && index !== certificateIndex.value)) { Message.warning('证书标识已存在'); return false; }
  certificateDraft.key = key;
  certificateDraft.namespace = namespace;
  certificateDraft.tls_secret_name = secretName;
  if (!certificateDraft.certificate_name) certificateDraft.certificate_name = key;
  if (certificateDraft.mode === 'cert-manager') form.value.components.cert_manager.enabled = true;
  if (certificateDraft.mode === 'uploaded-pem') {
    const certificatePEM = String(certificateDraft.certificate_pem || '').trim();
    const privateKeyPEM = String(certificateDraft.private_key_pem || '').trim();
    if (Boolean(certificatePEM) !== Boolean(privateKeyPEM)) { Message.warning('证书链和私钥必须同时填写'); return false; }
    if (!certificatePEM && !currentTLSMaterial.value) { Message.warning('请粘贴证书链和私钥'); return false; }
    certificateDraft.material_ref = key;
    if (certificatePEM) {
      certificateSaving.value = true;
      try {
        await store.saveTLSCertificate(key, certificatePEM, privateKeyPEM);
        if (scopeRevision !== store.scopeRevision) return false;
        tlsMaterialChanged.value = true;
        Message.success('证书和私钥校验通过，已加密保存');
      } catch (error: any) {
        Message.error(error.message);
        return false;
      } finally {
        certificateDraft.certificate_pem = '';
        certificateDraft.private_key_pem = '';
        certificateSaving.value = false;
      }
    }
  }
  if (scopeRevision !== store.scopeRevision) return false;
  const value = JSON.parse(JSON.stringify(certificateDraft));
  delete value.certificate_pem;
  delete value.private_key_pem;
  if (value.mode !== 'cert-manager') { delete value.domains; delete value.issuer_name; delete value.issuer_kind; }
  if (value.mode !== 'uploaded-pem') delete value.material_ref;
  if (certificateIndex.value >= 0) form.value.tls.certificates[certificateIndex.value] = value; else form.value.tls.certificates.push(value);
  return true;
};
const removeCertificate = (index: number) => {
  const certificate = form.value.tls.certificates[index];
  if (!certificate) return false;
  if ((form.value.domains || []).some((item: Dict) => item.certificate_ref === certificate.key)) { Message.warning('该证书仍被域名转发规则引用，请先调整域名配置'); return false; }
  form.value.tls.certificates.splice(index, 1);
  return true;
};
const syncDomainsFromIngress = async () => {
  if (!canSyncDomains.value) return false;
  const projectKey = store.currentProjectKey; const environmentKey = store.currentEnvironmentKey; const revision = store.scopeRevision;
  syncingDomains.value = true;
  try {
    const result = await api<IngressConfigSyncResponse>(
      `/api/projects/${encodeURIComponent(projectKey)}/environments/${encodeURIComponent(environmentKey)}/kubernetes/ingresses/sync-config`,
      { method: 'POST', timeoutMs: 120_000 },
    );
    if (revision !== store.scopeRevision) return true;
    store.config = result.config;
    const report = result.report;
    const changed = Number(report.updated_domains || 0);
    if (changed > 0) {
      Message.success(`同步完成：新增 ${report.imported_domains || 0} 个域名，更新 ${changed} 个域名，共回填 ${report.imported_routes || 0} 条路由`);
    } else {
      Message.info('平台域名转发配置已经与当前 EKS Ingress 一致');
    }
    if (report.skipped?.length) {
      Message.warning(`${report.skipped.length} 项因跨项目 Namespace、TLS 或路由冲突未自动覆盖，请到 Ingress 管理查看`);
    }
    return true;
  } catch (error: any) {
    if (revision === store.scopeRevision) Message.error(error.message || '同步 EKS Ingress 失败');
    return false;
  } finally {
    if (revision === store.scopeRevision) syncingDomains.value = false;
  }
};
const previewDomainsFromIngress = async () => {
  if (!canSyncDomains.value) return;
  const projectKey = store.currentProjectKey; const environmentKey = store.currentEnvironmentKey; const revision = store.scopeRevision;
  syncingDomains.value = true;
  try {
    const result = await api<IngressConfigSyncResponse>(
      `/api/projects/${encodeURIComponent(projectKey)}/environments/${encodeURIComponent(environmentKey)}/kubernetes/ingresses/sync-config?preview=true`,
      { method: 'POST', timeoutMs: 120_000 },
    );
    if (revision !== store.scopeRevision) return;
    const report = result.report;
    if (!report.updated_domains) {
      Message.info(report.skipped?.length
        ? `没有可安全同步的变更，${report.skipped.length} 项需要人工确认`
        : '平台域名转发配置已经与当前 EKS Ingress 一致');
      return;
    }
    Modal.confirm({
      title: '确认从 EKS 同步域名与转发规则？',
      content: `检测到 ${report.updated_domains} 个域名需要更新，其中新增 ${report.imported_domains || 0} 个域名、回填 ${report.imported_routes || 0} 条路由${report.skipped?.length ? `，另有 ${report.skipped.length} 项冲突会安全跳过` : ''}。同步只更新平台配置，不修改或删除 EKS 资源；平台中尚未部署到集群的规则也不会被删除。`,
      okText: '确认同步',
      cancelText: '取消',
      onOk: syncDomainsFromIngress,
    });
  } catch (error: any) {
    if (revision === store.scopeRevision) Message.error(error.message || '读取 EKS Ingress 差异失败');
  } finally {
    if (revision === store.scopeRevision) syncingDomains.value = false;
  }
};
const loadKubernetesServices = async (force = false) => {
  if ((!force && kubernetesServicesLoaded.value) || loadingKubernetesServices.value) return;
  const projectKey = store.currentProjectKey; const environmentKey = store.currentEnvironmentKey; const revision = store.scopeRevision;
  if (!projectKey || !environmentKey) { kubernetesServicesError.value = '请先选择项目和环境'; return; }
  loadingKubernetesServices.value = true; kubernetesServicesError.value = '';
  try {
    const response = await api<KubernetesServiceResponse>(`/api/projects/${encodeURIComponent(projectKey)}/environments/${encodeURIComponent(environmentKey)}/kubernetes/services`);
    if (revision !== store.scopeRevision) return;
    kubernetesServices.value = (response.services || []).filter((service) => service.name && service.namespace && service.ports?.some((port) => port.port > 0));
    kubernetesServicesLoaded.value = true;
    if (!domainDraft.namespace && kubernetesServiceNamespaces.value.length) domainDraft.namespace = kubernetesServiceNamespaces.value.includes('platform-server') ? 'platform-server' : kubernetesServiceNamespaces.value[0];
    const currentService = kubernetesServices.value.find((service) => service.namespace === domainDraft.namespace && service.name === domainDraft.service);
    const currentPort = Number(domainDraft.service_port);
    if (currentService?.ports.some((port) => port.port === currentPort)) domainDraft.service_port = currentPort;
    for (const route of Array.isArray(domainDraft.routes) ? domainDraft.routes : []) {
      const service = kubernetesServices.value.find((item) => item.namespace === domainDraft.namespace && item.name === route.service);
      const port = Number(route.service_port);
      if (service?.ports.some((item) => item.port === port)) route.service_port = port;
    }
  } catch (error: any) {
    if (revision !== store.scopeRevision) return;
    kubernetesServicesError.value = error.message || 'EKS Service 读取失败';
    if (!kubernetesServices.value.length) kubernetesServicesLoaded.value = false;
  } finally {
    if (revision === store.scopeRevision) loadingKubernetesServices.value = false;
  }
};
let domainRouteSequence = 0;
const newDomainRoute = (source: Dict = {}): Dict => ({
  _key: `domain-route-${++domainRouteSequence}`,
  path: String(source.path || '/'),
  path_type: String(source.path_type || 'Prefix'),
  service: String(source.service || ''),
  service_port: source.service_port ? Number(source.service_port) : undefined,
});
const openDomain = (index = -1) => {
  domainIndex.value = index;
  Object.keys(domainDraft).forEach((key) => delete domainDraft[key]);
  const current = index >= 0 ? JSON.parse(JSON.stringify(form.value.domains[index])) : { enabled: true, protocol: 'http', access_type: 'domain', domain: '', gateway: 'higress', namespace: '', service: '', service_port: undefined, path: '/', path_type: 'Prefix', routes: [], tls_enabled: false, certificate_ref: '', annotations: {}, tcp_scheme: 'internet-facing', allowed_cidrs: [] };
  if (!current.access_type) current.access_type = current.domain ? 'domain' : 'ip';
  if (!current.protocol) current.protocol = rawTCPServicePorts.has(Number(current.service_port)) ? 'tcp' : current.tls_enabled ? 'https' : 'http';
  if (current.protocol !== 'tcp') current.routes = domainRoutes(current).map((route) => newDomainRoute(route));
  if (!current.backend_protocol) {
    const annotations = current.annotations || {};
    const annotation = String(annotations['higress.io/backend-protocol'] || annotations['nginx.ingress.kubernetes.io/backend-protocol'] || '').toLowerCase();
    current.backend_protocol = ['grpc', 'grpcs'].includes(current.protocol) ? (annotation === 'grpcs' ? 'grpcs' : 'grpc') : current.protocol === 'wss' ? 'http' : (annotation === 'https' ? 'https' : 'http');
  }
  if (!current.certificate_ref && current.tls_secret_name) current.certificate_ref = form.value.tls.certificates.find((item: Dict) => item.tls_secret_name === current.tls_secret_name)?.key || '';
  if (current.protocol === 'tcp') {
    current.gateway = 'nlb'; current.tcp_scheme ||= 'internet-facing'; current.external_port ||= current.service_port;
    current.tls_enabled = false; current.certificate_ref = ''; current.tls_secret_name = '';
  }
  current.allowed_cidrs_text = (current.allowed_cidrs || []).join('\n');
  Object.assign(domainDraft, current);
  domainVisible.value = true;
  void loadKubernetesServices(true);
};
const changeDomainNamespace = () => {
  domainDraft.service = '';
  domainDraft.service_port = undefined;
  for (const route of Array.isArray(domainDraft.routes) ? domainDraft.routes : []) {
    route.service = '';
    route.service_port = undefined;
  }
};
const changeDomainProtocol = (value: unknown) => {
  const protocol = String(value);
  if (protocol === 'tcp') {
    const firstRoute = Array.isArray(domainDraft.routes) ? domainDraft.routes[0] : undefined;
    if (!domainDraft.service && firstRoute?.service) domainDraft.service = firstRoute.service;
    if (!domainDraft.service_port && firstRoute?.service_port) domainDraft.service_port = firstRoute.service_port;
    domainDraft.gateway = 'nlb'; domainDraft.tcp_scheme ||= 'internet-facing'; domainDraft.external_port ||= domainDraft.service_port;
    domainDraft.tls_enabled = false; domainDraft.certificate_ref = ''; domainDraft.tls_secret_name = '';
    return;
  }
  domainDraft.gateway = ['higress', 'nginx'].includes(String(domainDraft.gateway)) ? domainDraft.gateway : 'higress';
  if (!Array.isArray(domainDraft.routes) || !domainDraft.routes.length) domainDraft.routes = [newDomainRoute(domainDraft)];
  domainDraft.tls_enabled = ['https', 'wss', 'grpcs'].includes(protocol);
  if (['grpc', 'grpcs'].includes(protocol)) domainDraft.backend_protocol = ['grpc', 'grpcs'].includes(String(domainDraft.backend_protocol)) ? domainDraft.backend_protocol : 'grpc';
  else if (['grpc', 'grpcs'].includes(String(domainDraft.backend_protocol))) domainDraft.backend_protocol = 'http';
  if (domainDraft.tls_enabled && domainDraft.access_type === 'ip') domainDraft.access_type = 'domain';
  if (!domainDraft.tls_enabled) { domainDraft.certificate_ref = ''; domainDraft.tls_secret_name = ''; }
};
const changeDomainService = (value: unknown) => {
  const service = domainServiceOptions.value.find((item) => item.name === String(value));
  domainDraft.service_port = service?.ports[0]?.port;
  domainDraft.backend_protocol = inferBackendProtocol(service?.ports[0]);
  changeDomainServicePort(domainDraft.service_port);
};
const changeDomainServicePort = (value: unknown) => {
  const port = Number(value);
  const selectedPort = domainServicePorts.value.find((item) => item.port === port);
  if (routeProtocol(domainDraft) !== 'tcp') {
    const inferred = inferBackendProtocol(selectedPort);
    domainDraft.backend_protocol = isGRPCRoute.value ? 'grpc' : inferred;
  }
  if (rawTCPServicePorts.has(port) && routeProtocol(domainDraft) !== 'tcp' && !isGRPCRoute.value) {
    domainDraft.protocol = 'tcp'; domainDraft.external_port = port;
    changeDomainProtocol('tcp');
    Message.info(`端口 ${port} 是常见原生 TCP 端口，已自动切换为 AWS NLB`);
  } else if (routeProtocol(domainDraft) === 'tcp' && !domainDraft.external_port) domainDraft.external_port = port;
};
const domainRouteService = (route: Dict) => domainServiceOptions.value.find((item) => item.name === String(route.service || ''));
const domainRouteServicePorts = (route: Dict) => domainRouteService(route)?.ports || [];
const domainRouteServiceMissing = (route: Dict) => Boolean(route.service && kubernetesServicesLoaded.value && !domainRouteService(route));
const domainRoutePortMissing = (route: Dict) => Boolean(route.service_port && kubernetesServicesLoaded.value && !domainRouteServicePorts(route).some((item) => item.port === Number(route.service_port)));
const domainRouteHasNoEndpoint = (route: Dict) => {
  const service = domainRouteService(route);
  return Boolean(service?.endpoint_health_known && service.type !== 'ExternalName' && (service.ready_endpoints || 0) === 0);
};
const domainRouteHealthText = (route: Dict) => {
  const service = domainRouteService(route);
  if (!service || !service.endpoint_health_known || service.type === 'ExternalName') return '';
  return domainRouteHasNoEndpoint(route) ? '没有 Ready Endpoint' : `${service.ready_endpoints || 0} 个 Ready Endpoint`;
};
const changeHTTPRouteService = (route: Dict, value: unknown) => {
  const service = domainServiceOptions.value.find((item) => item.name === String(value));
  route.service_port = service?.ports[0]?.port;
  const inferred = inferBackendProtocol(service?.ports[0]);
  if (!isGRPCRoute.value && domainDraft.routes?.length === 1) domainDraft.backend_protocol = inferred;
};
const addDomainRoute = () => {
  if (!Array.isArray(domainDraft.routes)) domainDraft.routes = [];
  if (domainDraft.routes.length >= 64) { Message.warning('单个域名最多支持 64 条路径路由'); return; }
  domainDraft.routes.push(newDomainRoute());
};
const removeDomainRoute = (index: number) => {
  if (!Array.isArray(domainDraft.routes) || domainDraft.routes.length <= 1) return;
  domainDraft.routes.splice(index, 1);
};
const changeDomainAccessType = (value: string | number | boolean) => {
  if (String(value) !== 'ip') return;
  domainDraft.domain = '';
  if (routeIsSecure(domainDraft)) {
    domainDraft.protocol = routeProtocol(domainDraft) === 'wss' ? 'ws' : routeProtocol(domainDraft) === 'grpcs' ? 'grpc' : 'http';
    domainDraft.tls_enabled = false; domainDraft.certificate_ref = ''; domainDraft.tls_secret_name = '';
  }
};
const saveDomain = () => {
	if (domainDraft.access_type !== 'ip' && !String(domainDraft.domain || '').trim()) { Message.warning('域名访问方式必须填写域名'); return false; }
	if (loadingKubernetesServices.value) { Message.warning('正在读取 EKS Service，请稍候'); return false; }
	if (!kubernetesServicesLoaded.value) { Message.warning(kubernetesServicesError.value || '请先成功读取 EKS Service'); return false; }
	if (!domainDraft.namespace) { Message.warning('请选择后端 Service 所在的 Namespace'); return false; }
  if (isTCPRoute.value) {
    if (!domainDraft.service) { Message.warning('请选择 EKS 集群内的后端 Service'); return false; }
    const selected = kubernetesServices.value.find((item) => item.namespace === domainDraft.namespace && item.name === domainDraft.service);
    if (!selected) { Message.warning('所选后端 Service 已不在当前 EKS 集群中，请刷新后重新选择'); return false; }
    if (selected.type === 'ExternalName') { Message.warning('TCP NLB 不能直接对接 ExternalName Service，请选择带 Pod selector 的 Service'); return false; }
    if (selected.endpoint_health_known && (selected.ready_endpoints || 0) === 0) { Message.warning('所选 Service 没有 Ready Endpoint，无法创建 TCP 转发；请先恢复对应 Pod'); return false; }
    if (!selected.ports.some((item) => item.port === Number(domainDraft.service_port))) { Message.warning('请选择该 Service 实际暴露的端口'); return false; }
  } else {
    const routes = Array.isArray(domainDraft.routes) ? domainDraft.routes : [];
    if (!routes.length) { Message.warning('请至少添加一条路径转发规则'); return false; }
    const seenPaths = new Set<string>();
    for (let index = 0; index < routes.length; index += 1) {
      const route = routes[index];
      const path = String(route.path || '/').trim();
      if (!path.startsWith('/') || /[\r\n]/.test(path)) { Message.warning(`路由 ${index + 1} 的路径必须以 / 开头且不能换行`); return false; }
      if (seenPaths.has(path)) { Message.warning(`路由路径 ${path} 重复，请保留一条`); return false; }
      seenPaths.add(path);
      if (!['Prefix', 'Exact', 'ImplementationSpecific'].includes(String(route.path_type || 'Prefix'))) { Message.warning(`路由 ${index + 1} 的匹配方式不合法`); return false; }
      if (!route.service) { Message.warning(`请选择路由 ${index + 1} 的后端 Service`); return false; }
      const selected = kubernetesServices.value.find((item) => item.namespace === domainDraft.namespace && item.name === route.service);
      if (!selected) { Message.warning(`路由 ${index + 1} 的 Service 已不在当前 EKS 集群中，请刷新后重新选择`); return false; }
      if (selected.endpoint_health_known && selected.type !== 'ExternalName' && (selected.ready_endpoints || 0) === 0) { Message.warning(`路由 ${index + 1} 的 Service 没有 Ready Endpoint，请先恢复对应 Pod`); return false; }
      const selectedPort = selected.ports.find((item) => item.port === Number(route.service_port));
      if (!selectedPort) { Message.warning(`请选择路由 ${index + 1} 的 Service 实际暴露端口`); return false; }
      if (!isGRPCRoute.value && rawTCPServicePorts.has(selectedPort.port)) { Message.warning(`路由 ${index + 1} 的端口 ${selectedPort.port} 是原生 TCP 端口；请选择 TCP，或在确实提供 gRPC 时选择 gRPC 协议`); return false; }
      route.path = path;
      route.path_type ||= 'Prefix';
      route.service_port = Number(route.service_port);
    }
  }
	if (routeIsSecure(domainDraft) && !domainDraft.certificate_ref) { Message.warning('HTTPS/WSS/gRPCS 必须选择 TLS 证书配置'); return false; }
  if (isTCPRoute.value) {
    domainDraft.gateway = 'nlb'; domainDraft.tls_enabled = false; domainDraft.certificate_ref = ''; domainDraft.tls_secret_name = '';
    domainDraft.external_port = Number(domainDraft.external_port || domainDraft.service_port);
    const cidrs = String(domainDraft.allowed_cidrs_text || '').split(/[\s,]+/).map((item) => item.trim()).filter(Boolean);
    if (domainDraft.tcp_scheme === 'internet-facing' && !cidrs.length) { Message.warning('公网 TCP NLB 必须配置来源 CIDR 白名单'); return false; }
    if (cidrs.some((cidr) => cidr === '0.0.0.0/0' || cidr === '::/0')) { Message.warning('原生 TCP 服务不允许对全互联网开放'); return false; }
    domainDraft.allowed_cidrs = cidrs;
  } else {
    domainDraft.tls_enabled = routeIsSecure(domainDraft);
    setComponent(domainDraft.gateway === 'higress' ? 'components.catalog.higress.enabled' : 'components.catalog.nginx_ingress.enabled', true);
  }
  const certificate = form.value.tls.certificates.find((item: Dict) => item.key === domainDraft.certificate_ref);
  domainDraft.tls_secret_name = certificate?.tls_secret_name || '';
  domainDraft.annotations = { ...(domainDraft.annotations || {}) };
  delete domainDraft.annotations['higress.io/backend-protocol'];
  delete domainDraft.annotations['nginx.ingress.kubernetes.io/backend-protocol'];
  const backendProtocol = !isTCPRoute.value ? String(domainDraft.backend_protocol || 'http').toLowerCase() : '';
  if (['https', 'grpc', 'grpcs'].includes(backendProtocol)) domainDraft.annotations[domainDraft.gateway === 'higress' ? 'higress.io/backend-protocol' : 'nginx.ingress.kubernetes.io/backend-protocol'] = backendProtocol.toUpperCase();
  const value = JSON.parse(JSON.stringify(domainDraft));
  delete value.allowed_cidrs_text;
  if (routeProtocol(value) !== 'tcp') {
    delete value.allowed_cidrs; delete value.tcp_scheme; delete value.external_port;
    value.routes = value.routes.map((route: Dict) => {
      const normalized = { path: route.path || '/', path_type: route.path_type || 'Prefix', service: route.service, service_port: Number(route.service_port) };
      return normalized;
    });
    const first = value.routes[0];
    value.path = first.path; value.path_type = first.path_type; value.service = first.service; value.service_port = first.service_port;
  } else {
    delete value.path; delete value.path_type; delete value.backend_protocol; delete value.routes;
  }
  if (domainIndex.value >= 0) form.value.domains[domainIndex.value] = value; else form.value.domains.push(value);
  return true;
};
watch(
  [() => route.query.domain_index, () => form.value.domains?.length],
  ([requestedIndex]) => {
    if (requestedIndex === undefined || requestedIndex === null || activeTab.value !== 'domains') return;
    const index = Number(requestedIndex);
    if (!Number.isInteger(index) || index < 0 || index >= (form.value.domains?.length || 0)) {
      Message.warning('部署配置中的域名规则已经变化，请从列表重新选择');
    } else {
      openDomain(index);
    }
    const query = { ...route.query };
    delete query.domain_index;
    void router.replace({ query });
  },
  { flush: 'post' },
);
const addChannel = () => {
  const address = String(channelDraft.address || '').trim();
  const valid = channelDraft.type === 'email' ? /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(address) : /^https?:\/\/[^\s]+$/i.test(address);
  if (!channelDraft.name || !valid) { Message.warning(channelDraft.type === 'email' ? '请填写名称和合法邮箱地址' : '请填写名称和完整的 HTTP(S) 接收地址'); return false; }
  form.value.alerting.channels.push({ ...channelDraft, address });
	  form.value.alerting.enabled = true;
	  Object.assign(channelDraft, { name: '', type: 'lark', address: '', secret_ref: '' });
  return true;
};
const testAlertChannel = async (record: Dict) => {
  if (dirty.value) { Message.warning('请先保存环境配置，再测试告警通道'); return; }
  if (!store.currentProjectKey || !store.currentEnvironmentKey || testingAlertChannel.value) return;
  testingAlertChannel.value = String(record.name || '');
  try {
    const response = await api<{ message: string }>(`/api/projects/${encodeURIComponent(store.currentProjectKey)}/environments/${encodeURIComponent(store.currentEnvironmentKey)}/alerting/channels/${encodeURIComponent(String(record.name || ''))}/test`, { method: 'POST' });
    Message.success(response.message || `测试消息已发送到 ${record.name}`);
  } catch (error: any) {
    Message.error(error.message || '告警通道测试发送失败');
  } finally {
    testingAlertChannel.value = '';
  }
};
type AlertScenarioResult = { scenario: string; template: string; delivered: string[]; failed: string[] };
const sendAlertScenario = (scenario: string) => api<AlertScenarioResult>(`/api/projects/${encodeURIComponent(store.currentProjectKey)}/environments/${encodeURIComponent(store.currentEnvironmentKey)}/alerting/scenarios/${encodeURIComponent(scenario)}/test`, { method: 'POST' });
const testAlertScenario = async (scenario: string) => {
  if (dirty.value) { Message.warning('请先保存环境配置，再测试告警场景'); return; }
  if (!store.currentProjectKey || !store.currentEnvironmentKey || testingAlertScenario.value) return;
  const definition = alertScenarios.find((item) => item.key === scenario);
  testingAlertScenario.value = scenario;
  try {
    const response = await sendAlertScenario(scenario);
    const template = response.template ? `，模板 ${response.template}` : '';
    const partial = response.failed?.length ? `；${response.failed.length} 个通道失败：${response.failed.join('、')}` : '';
    const summary = `${definition?.name || scenario}测试已送达 ${response.delivered?.length || 0} 个通道${template}${partial}`;
    if (response.failed?.length) Message.warning(summary); else Message.success(summary);
  } catch (error: any) {
    Message.error(error.message || `${definition?.name || scenario}场景测试失败`);
  } finally {
    testingAlertScenario.value = '';
  }
};
const testAllAlertScenarios = async () => {
  if (dirty.value) { Message.warning('请先保存环境配置，再测试告警场景'); return; }
  if (!store.currentProjectKey || !store.currentEnvironmentKey || testingAlertScenario.value) return;
  testingAlertScenario.value = 'all';
  const succeeded: string[] = []; const failed: string[] = []; const partial: string[] = [];
  try {
    for (const scenario of alertScenarios) {
      try {
        const response = await sendAlertScenario(scenario.key);
        succeeded.push(scenario.name);
        if (response.failed?.length) partial.push(`${scenario.name}（${response.failed.join('、')}）`);
      } catch {
        failed.push(scenario.name);
      }
    }
    if (!failed.length && !partial.length) Message.success(`${alertScenarios.length} 类 Markdown 告警全部发送成功：${succeeded.join('、')}`);
    else Message.warning(`完整送达 ${succeeded.length - partial.length} 类，部分失败 ${partial.length} 类，全部失败 ${failed.length} 类${partial.length ? `；部分失败：${partial.join('、')}` : ''}${failed.length ? `；全部失败：${failed.join('、')}` : ''}`);
  } finally {
    testingAlertScenario.value = '';
  }
};
const addTemplate = () => { if (!templateDraft.name || !templateDraft.title) { Message.warning('名称和标题不能为空'); return false; } form.value.alerting.templates.push({ ...templateDraft, format: 'markdown' }); Object.assign(templateDraft, { name: '', event_type: 'custom', severity: 'warning', format: 'markdown', title: '', body: '' }); return true; };
const restoreDefaultAlertTemplates = () => {
  const presetNames = new Set(defaultAlertTemplates.map((item) => item.name));
  const customTemplates = (form.value.alerting.templates || []).filter((item: Dict) => !presetNames.has(String(item.name || '')));
  form.value.alerting.templates = [...JSON.parse(JSON.stringify(defaultAlertTemplates)), ...customTemplates];
  form.value.alerting.template_preset_version = 6;
  Message.success(`已应用 ${defaultAlertTemplates.length} 个新版 Markdown 告警模板；自定义模板已保留，请保存配置后测试`);
};
const resetDeleteEnvironment = () => { deleteConfirm.value = ''; deleteDestroyResources.value = false; deleteDestroyConfirm.value = ''; deletePassword.value = ''; };
const openDeleteEnvironment = () => { resetDeleteEnvironment(); deleteVisible.value = true; };
const deleteEnvironment = async () => {
  if (deleteConfirm.value !== scopeName.value) { Message.warning('环境确认内容不匹配'); return false; }
  if (deleteDestroyResources.value && deleteDestroyConfirm.value !== `destroy:${scopeName.value}`) { Message.warning('销毁确认内容不匹配'); return false; }
  if (deleteDestroyResources.value && !deletePassword.value) { Message.warning('请输入当前账号密码'); return false; }
  try {
    const job = await store.deleteEnvironment({ destroyResources: deleteDestroyResources.value, destroyConfirm: deleteDestroyConfirm.value, password: deletePassword.value });
    deletePassword.value = '';
    if (job) {
      Message.success('已提交销毁任务；资源清空后会自动删除环境');
      await router.push({ name: 'jobs', query: { job: job.id } });
    } else {
      Message.success('环境配置已删除');
    }
    resetDeleteEnvironment();
    return true;
  } catch (error:any) {
    deletePassword.value = '';
    Message.error(error.message);
    return false;
  }
};
const destroyEnvironment = async () => { const confirm = `destroy:${scopeName.value}`; if (destroyConfirm.value !== confirm) { Message.warning('销毁确认不匹配'); return false; } if (!destroyPassword.value) { Message.warning('请输入当前账号密码'); return false; } try { const job = await store.createJob('destroy', confirm, destroyPassword.value); destroyConfirm.value = ''; destroyPassword.value = ''; router.push({ name: 'jobs', query: { job: job.id } }); return true; } catch (error:any) { destroyPassword.value = ''; Message.error(error.message); return false; } };
</script>
