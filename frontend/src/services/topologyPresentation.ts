import type { ApplicationTopologyNode } from '@/types';

export type TopologyIconFamily =
  | 'domain'
  | 'gateway'
  | 'service'
  | 'workload'
  | 'database'
  | 'cache'
  | 'queue'
  | 'storage'
  | 'observability'
  | 'delivery'
  | 'registry'
  | 'generic';

export type TopologyArchitectureZone = 'edge' | 'service' | 'workload' | 'data' | 'operations';

export interface TopologyNodePresentation {
  family: TopologyIconFamily;
  badge: string;
  title: string;
  color: string;
}

type Signature = {
  pattern: RegExp;
  family: TopologyIconFamily;
  badge: string;
  title: string;
  color: string;
};

// Match concrete products before falling back to Kubernetes resource kinds.
// The list deliberately uses names and non-secret labels already returned by
// the topology API, so adding a new project does not require drawing a graph by hand.
const signatures: Signature[] = [
  { pattern: /\b(mysql|mariadb|aurora)\b/i, family: 'database', badge: 'MY', title: 'MySQL / Aurora', color: '#2f81d5' },
  { pattern: /\b(postgres|postgresql|pgsql)\b/i, family: 'database', badge: 'PG', title: 'PostgreSQL', color: '#3d6ea8' },
  { pattern: /\b(mongo|mongodb)\b/i, family: 'database', badge: 'MO', title: 'MongoDB', color: '#25a45a' },
  { pattern: /\b(clickhouse)\b/i, family: 'database', badge: 'CH', title: 'ClickHouse', color: '#d8a900' },
  { pattern: /\b(elastic|elasticsearch)\b/i, family: 'database', badge: 'ES', title: 'Elasticsearch', color: '#7a5ac8' },
  { pattern: /\b(redis|valkey)\b/i, family: 'cache', badge: 'RD', title: 'Redis / Valkey', color: '#d84a4a' },
  { pattern: /\b(kafka)\b/i, family: 'queue', badge: 'KF', title: 'Kafka', color: '#343d4a' },
  { pattern: /\b(rabbitmq|rabbit-mq)\b/i, family: 'queue', badge: 'MQ', title: 'RabbitMQ', color: '#ef7b22' },
  { pattern: /\b(activemq|active-mq)\b/i, family: 'queue', badge: 'AM', title: 'ActiveMQ', color: '#b73b4b' },
  { pattern: /\b(etcd)\b/i, family: 'registry', badge: 'ET', title: 'etcd', color: '#4c7bd9' },
  { pattern: /\b(consul)\b/i, family: 'registry', badge: 'CO', title: 'Consul', color: '#d7438b' },
  { pattern: /\b(nacos)\b/i, family: 'registry', badge: 'NA', title: 'Nacos', color: '#2785e4' },
  { pattern: /\b(s3|object[-_ ]?storage|minio)\b/i, family: 'storage', badge: 'S3', title: '对象存储', color: '#3f8f54' },
  { pattern: /\b(prometheus)\b/i, family: 'observability', badge: 'PM', title: 'Prometheus', color: '#e65a32' },
  { pattern: /\b(grafana)\b/i, family: 'observability', badge: 'GF', title: 'Grafana', color: '#ef8d21' },
  { pattern: /\b(loki)\b/i, family: 'observability', badge: 'LK', title: 'Loki', color: '#5f58c7' },
  { pattern: /\b(kibana)\b/i, family: 'observability', badge: 'KB', title: 'Kibana', color: '#c44991' },
  { pattern: /\b(clickvisual)\b/i, family: 'observability', badge: 'CV', title: 'ClickVisual', color: '#337bd8' },
  { pattern: /\b(jenkins)\b/i, family: 'delivery', badge: 'JK', title: 'Jenkins', color: '#b24a42' },
  { pattern: /\b(gitlab)\b/i, family: 'delivery', badge: 'GL', title: 'GitLab', color: '#e9632e' },
  { pattern: /\b(argocd|argo-cd)\b/i, family: 'delivery', badge: 'AR', title: 'Argo CD', color: '#e9725b' },
  { pattern: /\b(tekton)\b/i, family: 'delivery', badge: 'TK', title: 'Tekton', color: '#d83963' },
  { pattern: /\b(higress)\b/i, family: 'gateway', badge: 'HG', title: 'Higress 网关', color: '#2f76dd' },
  { pattern: /\b(nginx|ingress-nginx)\b/i, family: 'gateway', badge: 'NX', title: 'Nginx 网关', color: '#159454' },
];

const nodeText = (node: ApplicationTopologyNode) => [
  node.name,
  node.namespace,
  node.kind,
  ...node.services,
  ...node.hosts,
  ...Object.entries(node.labels || {}).flatMap(([key, value]) => [key, value]),
].join(' ').replace(/[_/.:]+/g, ' ');

export const topologyNodePresentation = (node: ApplicationTopologyNode): TopologyNodePresentation => {
  const identity = nodeText(node);
  const matched = signatures.find((signature) => signature.pattern.test(identity));
  if (matched) {
    return {
      family: matched.family,
      badge: matched.badge,
      title: matched.title,
      color: matched.color,
    };
  }

  if (node.kind === 'Domain') return { family: 'domain', badge: 'DNS', title: '域名入口', color: '#d78919' };
  if (node.kind === 'Gateway' || node.kind === 'Ingress') return { family: 'gateway', badge: 'GW', title: '网关', color: '#e66b37' };
  if (node.kind === 'Service') return { family: 'service', badge: 'SVC', title: 'Kubernetes Service', color: '#169bb4' };
  if (node.kind === 'StatefulSet') return { family: 'database', badge: 'STS', title: 'StatefulSet', color: '#7364c8' };
  if (node.kind === 'DaemonSet') return { family: 'workload', badge: 'DS', title: 'DaemonSet', color: '#596fc2' };
  if (node.kind === 'Deployment') return { family: 'workload', badge: 'DEP', title: 'Deployment', color: '#5573d9' };
  return { family: 'generic', badge: 'APP', title: node.kind || '应用对象', color: '#637b91' };
};

export const topologyNodeStage = (node: ApplicationTopologyNode) => {
  if (node.layer === 'data') return 'data' as const;
  if (node.kind === 'Gateway') return 'gateway' as const;
  if (node.kind === 'Domain' || node.kind === 'Ingress') return 'domain' as const;
  if (node.kind === 'Service') return 'service' as const;
  return 'workload' as const;
};

export const topologyArchitectureZone = (node: ApplicationTopologyNode): TopologyArchitectureZone => {
  if (node.kind === 'Gateway' || node.kind === 'Domain' || node.kind === 'Ingress') return 'edge';
  const presentation = topologyNodePresentation(node);
  if (presentation.family === 'observability' || presentation.family === 'delivery') return 'operations';
  if (node.layer === 'data') return 'data';
  if (node.kind === 'Service') return 'service';
  return 'workload';
};
