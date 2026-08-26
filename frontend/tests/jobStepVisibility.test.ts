import assert from 'node:assert/strict';
import test from 'node:test';
import { visibleJobSteps } from '../src/services/jobStepVisibility.ts';

const step = (name: string, status: 'pending' | 'running' | 'succeeded' | 'failed' = 'succeeded') => ({ name, status });

test('task cards hide optional steps that are not selected', () => {
  const steps = [
    step('更新当前环境 EKS 访问配置'),
    step('检查 etcd Helm Chart'),
    step('检查 XXL-JOB Helm Chart'),
    step('检查 Nacos Helm Chart'),
    step('同步 Consul 客户端 CA'),
    step('配置 Alertmanager 告警路由'),
    step('同步网关负载均衡地址'),
    step('创建或更新 Kubernetes TLS Secret'),
    step('验证集群日志采集器'),
  ];
  const config = {
    namespaces: {},
    components: {
      consul: { enabled: false }, etcd: { enabled: false },
      catalog: {
        xxl_job: { enabled: false }, nacos: { enabled: false }, prometheus: { enabled: false },
        higress: { enabled: false }, loki: { enabled: false },
      },
    },
    alerting: { enabled: false }, tls: { certificates: [] },
  };
  assert.deepEqual(visibleJobSteps(steps, 'platform', config).map((item) => item.name), ['更新当前环境 EKS 访问配置']);
});

test('task cards show only enabled component and configuration steps', () => {
  const steps = [
    step('检查 etcd Helm Chart'), step('检查 XXL-JOB Helm Chart'), step('检查 Nacos Helm Chart'),
    step('同步 Consul 客户端 CA'), step('配置 Alertmanager 告警路由'),
    step('同步网关负载均衡地址'), step('创建或更新 Kubernetes TLS Secret'),
    step('验证 Loki 日志写入与查询'), step('验证 Grafana 默认中文 Dashboard'),
  ];
  const config = {
    namespaces: { 'platform-server': { enabled: true }, 'app-test': { enabled: true } },
    components: {
      consul: { enabled: true, namespace: 'platform-server' }, etcd: { enabled: true },
      catalog: {
        xxl_job: { enabled: false }, nacos: { enabled: true }, prometheus: { enabled: true },
        higress: { enabled: true }, loki: { enabled: true },
      },
    },
    alerting: { enabled: true },
    tls: { certificates: [{ enabled: true, mode: 'uploaded-pem' }] },
  };
  assert.deepEqual(visibleJobSteps(steps, 'platform', config).map((item) => item.name), [
    '检查 etcd Helm Chart', '检查 Nacos Helm Chart', '同步 Consul 客户端 CA',
    '配置 Alertmanager 告警路由', '同步网关负载均衡地址', '创建或更新 Kubernetes TLS Secret',
    '验证 Loki 日志写入与查询', '验证 Grafana 默认中文 Dashboard',
  ]);
});

test('running and failed optional steps remain visible for diagnosis', () => {
  const config = { components: { etcd: { enabled: false }, catalog: { higress: { enabled: false } } } };
  const steps = [step('检查 etcd Helm Chart', 'failed'), step('同步网关负载均衡地址', 'running')];
  assert.deepEqual(visibleJobSteps(steps, 'platform', config), steps);
});

test('explicit TLS jobs keep cleanup cards even after the certificate was removed', () => {
  const steps = [step('确保 TLS 目标 Namespace 可用'), step('创建或更新 Kubernetes TLS Secret')];
  assert.equal(visibleJobSteps(steps, 'tls', { tls: { certificates: [] } }).length, 2);
});
