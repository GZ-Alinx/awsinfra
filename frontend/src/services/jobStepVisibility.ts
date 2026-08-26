type Dict = Record<string, any>;

export interface VisibleJobStep {
  name: string;
  status: 'pending' | 'running' | 'succeeded' | 'failed';
  [key: string]: any;
}

function pathValue(config: Dict, path: string): any {
  return path.split('.').reduce<any>((value, key) => value && typeof value === 'object' ? value[key] : undefined, config);
}

function enabled(config: Dict, path: string): boolean {
  return pathValue(config, path) === true;
}

function hasConsulClientNamespaces(config: Dict): boolean {
  if (!enabled(config, 'components.consul.enabled')) return false;
  const source = String(pathValue(config, 'components.consul.namespace') || 'platform-server').trim();
  const namespaces = pathValue(config, 'namespaces');
  if (!namespaces || typeof namespaces !== 'object' || Array.isArray(namespaces)) return false;
  return Object.entries(namespaces).some(([name, value]) => {
    if (!name.trim() || name.trim() === source) return false;
    return !value || typeof value !== 'object' || (value as Dict).enabled !== false;
  });
}

function hasUploadedTLSCertificate(config: Dict): boolean {
  const certificates = pathValue(config, 'tls.certificates');
  return Array.isArray(certificates) && certificates.some((certificate) =>
    certificate && typeof certificate === 'object'
      && certificate.enabled !== false
      && certificate.mode === 'uploaded-pem',
  );
}

const optionalStepVisibility: Record<string, (config: Dict, action: string) => boolean> = {
  '检查 etcd Helm Chart': (config) => enabled(config, 'components.etcd.enabled'),
  '检查 XXL-JOB Helm Chart': (config) => enabled(config, 'components.catalog.xxl_job.enabled'),
  '检查 Nacos Helm Chart': (config) => enabled(config, 'components.catalog.nacos.enabled'),
  '同步 Consul 客户端 CA': (config) => hasConsulClientNamespaces(config),
  '配置 Alertmanager 告警路由': (config) => enabled(config, 'alerting.enabled') && enabled(config, 'components.catalog.prometheus.enabled'),
  '同步网关负载均衡地址': (config) => enabled(config, 'components.catalog.higress.enabled'),
  '创建或更新 Kubernetes TLS Secret': (config, action) => action === 'tls' || hasUploadedTLSCertificate(config),
  '验证集群日志采集器': (config) => enabled(config, 'components.catalog.loki.enabled'),
  '验证 Loki 日志写入与查询': (config) => enabled(config, 'components.catalog.loki.enabled'),
  '验证 Grafana Loki 数据源': (config) => enabled(config, 'components.catalog.loki.enabled'),
  '验证 Grafana 默认中文 Dashboard': (config) => enabled(config, 'components.catalog.prometheus.enabled'),
};

/**
 * Deployment jobs include a few optional maintenance steps. The task console
 * should only present cards that belong to the selected environment config.
 * Running and failed steps are always retained so an operational problem can
 * never disappear merely because the related switch was changed afterwards.
 */
export function visibleJobSteps<T extends VisibleJobStep>(steps: T[] | undefined, action: string, config: Dict | null): T[] {
  if (!steps?.length) return [];
  if (!config) return steps;
  return steps.filter((step) => {
    if (step.status === 'running' || step.status === 'failed') return true;
    const predicate = optionalStepVisibility[step.name];
    return !predicate || predicate(config, action);
  });
}
