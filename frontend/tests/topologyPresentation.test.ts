import assert from 'node:assert/strict';
import test from 'node:test';
import {
  topologyArchitectureZone,
  topologyNodePresentation,
  topologyNodeStage,
} from '../src/services/topologyPresentation.ts';
import type { ApplicationTopologyNode } from '../src/types.ts';

const node = (name: string, kind = 'Service', layer = 'application', labels: Record<string, string> = {}): ApplicationTopologyNode => ({
  id: `${kind}:${name}`,
  name,
  namespace: 'demo-test',
  kind,
  layer,
  state: 'normal',
  state_reason: 'ok',
  services: [],
  ports: [],
  hosts: [],
  labels,
});

test('recognizes common middleware and database icons from real resource identity', () => {
  assert.deepEqual(topologyNodePresentation(node('game-redis')).family, 'cache');
  assert.equal(topologyNodePresentation(node('orders-mysql')).badge, 'MY');
  assert.equal(topologyNodePresentation(node('event-kafka')).badge, 'KF');
  assert.equal(topologyNodePresentation(node('runtime', 'Deployment', 'application', { 'app.kubernetes.io/name': 'grafana' })).badge, 'GF');
});

test('falls back to Kubernetes resource role and preserves topology stage', () => {
  assert.deepEqual(topologyNodePresentation(node('orders', 'Deployment')).family, 'workload');
  assert.equal(topologyNodePresentation(node('public.example.com', 'Domain')).badge, 'DNS');
  assert.equal(topologyNodeStage(node('redis', 'Service', 'data')), 'data');
  assert.equal(topologyNodeStage(node('orders', 'Deployment')), 'workload');
});

test('maps new projects into architecture zones without static project configuration', () => {
  assert.equal(topologyArchitectureZone(node('api.example.com', 'Domain')), 'edge');
  assert.equal(topologyArchitectureZone(node('orders', 'Service')), 'service');
  assert.equal(topologyArchitectureZone(node('orders', 'Deployment')), 'workload');
  assert.equal(topologyArchitectureZone(node('redis', 'Service', 'data')), 'data');
  assert.equal(topologyArchitectureZone(node('prometheus', 'Deployment')), 'operations');
});
