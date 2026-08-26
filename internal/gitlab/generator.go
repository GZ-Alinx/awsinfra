package gitlab

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

var deliveryEnvironments = []string{"dev", "test", "uat", "prod"}

func validDeliveryEnvironment(value string) bool {
	for _, environment := range deliveryEnvironments {
		if value == environment {
			return true
		}
	}
	return false
}

func generateDeliveryFiles(project string, delivery ProjectDelivery, server Server) []GeneratedFile {
	files := []GeneratedFile{}
	marker := func(kind string) string {
		payload, _ := json.MarshalIndent(map[string]any{
			"schema": 1, "managed_by": "ops-deploy-platform", "project": project, "repository_kind": kind,
		}, "", "  ")
		return string(payload) + "\n"
	}
	jenkinsRepository, manifestRepository := "jenkinsfiles", "manifests"
	if unifiedDelivery(delivery) {
		jenkinsRepository, manifestRepository = "delivery", "delivery"
		files = append(files,
			GeneratedFile{Repository: "delivery", Path: ".ops-deploy/managed.json", Content: marker("delivery")},
			GeneratedFile{Repository: "delivery", Path: "README.md", Content: deliveryReadme(project)},
		)
	} else {
		files = append(files,
			GeneratedFile{Repository: "jenkinsfiles", Path: ".ops-deploy/managed.json", Content: marker("jenkinsfiles")},
			GeneratedFile{Repository: "jenkinsfiles", Path: "README.md", Content: jenkinsfilesReadme(project)},
			GeneratedFile{Repository: "manifests", Path: ".ops-deploy/managed.json", Content: marker("deploy-manifests")},
			GeneratedFile{Repository: "manifests", Path: "README.md", Content: manifestsReadme(project)},
		)
	}
	services := append([]ServiceSpec(nil), delivery.Services...)
	sort.Slice(services, func(i, j int) bool { return services[i].Key < services[j].Key })
	for _, environment := range deliveryEnvironments {
		for _, service := range services {
			if service.ManifestMode != "repository" {
				prefix := "environments/" + environment + "/" + service.Key + "/"
				files = append(files, GeneratedFile{Repository: manifestRepository, Path: prefix + "manifest.yaml", Content: renderManifest(project, environment, service)})
			}
			if dockerfileSource(service) == "platform" {
				files = append(files, GeneratedFile{
					Repository: jenkinsRepository,
					Path:       managedDockerfilePath(environment, service.Key),
					Content:    dockerfileContentForEnvironment(service, environment),
				})
			}
		}
	}
	return files
}

func unifiedDelivery(delivery ProjectDelivery) bool {
	return delivery.JenkinsfilesProjectPath != "" && delivery.JenkinsfilesProjectPath == delivery.ManifestsProjectPath
}

// legacyKustomizationFiles describes only the exact files emitted by platform
// versions that used `kubectl apply -k`. It is used to remove that obsolete
// layer without deleting a user-maintained Kustomize file whose content has
// changed.
func legacyKustomizationFiles(delivery ProjectDelivery) []GeneratedFile {
	services := append([]ServiceSpec(nil), delivery.Services...)
	sort.Slice(services, func(i, j int) bool { return services[i].Key < services[j].Key })
	files := make([]GeneratedFile, 0, len(deliveryEnvironments)*(len(services)+1))
	for _, environment := range deliveryEnvironments {
		var root strings.Builder
		root.WriteString("resources:\n")
		for _, service := range services {
			files = append(files, GeneratedFile{Path: "environments/" + environment + "/" + service.Key + "/kustomization.yaml", Content: "resources:\n  - manifest.yaml\n"})
			fmt.Fprintf(&root, "  - %s\n", service.Key)
		}
		files = append(files, GeneratedFile{Path: "environments/" + environment + "/kustomization.yaml", Content: root.String()})
	}
	return files
}

func jenkinsfilesReadme(project string) string {
	return "# " + project + " 部署流水线\n\n本仓库仅属于项目 `" + project + "`，由运维自动部署平台管理。\n\n```text\nenvironments/<env>/pipelines/<job>/Jenkinsfile\nenvironments/<env>/pipelines/<job>/services.groovy\nenvironments/<env>/dockerfiles/<service>/Dockerfile\nlib/v4/opsPipeline.groovy\nlib/v4/scripts/\n```\n\n- Jenkinsfile 与平台托管 Dockerfile 都按 dev/test/uat/prod 环境隔离。\n- 一个 Job 可以关联多个服务，但一次 Jenkins 构建只处理一个服务。\n- 依赖安装、编译、测试和镜像内容由 Dockerfile 负责；Jenkinsfile 不重复实现业务构建。\n- 服务也可选择直接使用业务源码仓库内的 Dockerfile。\n- 仓库地址、分支、Namespace、镜像仓库和 Credential ID 来自平台配置；Token、密码等明文不得写入本仓库。\n"
}

func manifestsReadme(project string) string {
	return "# " + project + " 部署清单\n\n本仓库仅属于项目 `" + project + "`，按环境和服务目录隔离。\n\n```text\nenvironments/\n  dev|test|uat|prod/\n    <service>/\n      manifest.yaml\n```\n\n每个服务在每个环境只有一份部署清单，Jenkins 仅替换 `{{IMAGE}}` 和可选的 `{{ETCD_PASSWORD}}`，然后执行 `kubectl apply -f environments/<env>/<service>/manifest.yaml`。\n"
}

func deliveryReadme(project string) string {
	return "# " + project + " 运维交付仓库\n\n本仓库由运维自动部署平台管理，通过环境目录保存 Jenkinsfile、Dockerfile 与 Kubernetes 部署清单。\n\n```text\nenvironments/\n  dev|test|uat|prod/\n    pipelines/<job>/Jenkinsfile\n    pipelines/<job>/services.groovy\n    dockerfiles/<service>/Dockerfile\n    <service>/manifest.yaml\nlib/                         # 平台流水线公共模块\n```\n\n- Job 固定绑定一个环境，不能在构建时切换到其他环境。\n- Jenkinsfile、Dockerfile 与部署清单位于同一仓库、同一环境目录，test/prod 互不覆盖。\n- Token、密码等秘密不得写入仓库。\n"
}

func renderServiceMetadata(service ServiceSpec) string {
	return fmt.Sprintf("service: %s\ndisplayName: %s\nworkloadType: %s\nlanguage: %s\nruntimeVersion: %s\nmanifestMode: %s\nsource:\n  repository: %s\n  branch: %s\nbuild:\n  context: %s\n  dockerfileSource: %s\n  dockerfile: %s\n  dockerTarget: %s\nimageRepository: %s\nnamespace: %s\ncontainerPort: %d\nreplicas: %d\n",
		service.Key, yamlScalar(service.DisplayName), service.WorkloadType, service.Language, yamlScalar(service.RuntimeVersion), service.ManifestMode, yamlScalar(service.SourceRepository), yamlScalar(service.SourceBranch), yamlScalar(service.BuildContext), dockerfileSource(service), yamlScalar(service.Dockerfile), yamlScalar(service.DockerTarget), yamlScalar(service.ImageRepository), service.Namespace, service.ContainerPort, service.Replicas)
}

func dockerfileSource(service ServiceSpec) string {
	if strings.EqualFold(strings.TrimSpace(service.DockerfileSource), "source") {
		return "source"
	}
	return "platform"
}

func managedDockerfilePath(environment, serviceKey string) string {
	return "environments/" + strings.ToLower(strings.TrimSpace(environment)) + "/dockerfiles/" + strings.ToLower(strings.TrimSpace(serviceKey)) + "/Dockerfile"
}

func dockerfileContent(service ServiceSpec) string {
	if content := strings.TrimSpace(service.DockerfileContent); content != "" {
		return content + "\n"
	}
	return defaultDockerfile(service)
}

func dockerfileContentForEnvironment(service ServiceSpec, environment string) string {
	if content := strings.TrimSpace(service.DockerfileContents[strings.ToLower(strings.TrimSpace(environment))]); content != "" {
		return content + "\n"
	}
	return dockerfileContent(service)
}

func defaultDockerfile(service ServiceSpec) string {
	runtimeVersion := strings.TrimSpace(service.RuntimeVersion)
	switch service.Language {
	case "java":
		if runtimeVersion == "" {
			runtimeVersion = "21"
		}
		return fmt.Sprintf(`# syntax=docker/dockerfile:1.7
FROM maven:3.9-eclipse-temurin-%s AS build
WORKDIR /src
COPY . .
RUN mvn -B -DskipTests package && mkdir -p /out && cp "$(find target -maxdepth 1 -type f -name '*.jar' ! -name '*sources*' ! -name '*javadoc*' | head -n 1)" /out/app.jar

FROM eclipse-temurin:%s-jre
WORKDIR /app
COPY --from=build /out/app.jar /app/app.jar
USER 10001:10001
EXPOSE %d
ENTRYPOINT ["java", "-jar", "/app/app.jar"]
`, runtimeVersion, runtimeVersion, service.ContainerPort)
	case "node":
		if runtimeVersion == "" {
			runtimeVersion = "20"
		}
		if service.WorkloadType == "frontend" {
			healthPath := strings.TrimSpace(service.HealthPath)
			if healthPath == "" {
				healthPath = "/healthz"
			}
			return fmt.Sprintf(`# syntax=docker/dockerfile:1.7
FROM node:%s-alpine AS build
WORKDIR /src
COPY package*.json ./
RUN if [ -f package-lock.json ]; then npm ci --ignore-scripts --no-audit --no-fund; else npm install --ignore-scripts --no-audit --no-fund; fi
COPY . .
RUN npm run build && \
    mkdir -p /out && \
    printf '%%s\n' \
      'server {' \
      '  listen %d default_server;' \
      '  listen [::]:%d default_server;' \
      '  server_name _;' \
      '  server_tokens off;' \
      '  root /usr/share/nginx/html;' \
      '  index index.html;' \
      '  location = %s { access_log off; default_type text/plain; return 200 "ok\n"; }' \
      '  location / { try_files $uri $uri/ /index.html; }' \
      '}' > /out/default.conf

FROM nginxinc/nginx-unprivileged:1.29-alpine
COPY --from=build /out/default.conf /etc/nginx/conf.d/default.conf
COPY --from=build /src/dist/ /usr/share/nginx/html/
USER 101:101
EXPOSE %d
`, runtimeVersion, service.ContainerPort, service.ContainerPort, healthPath, service.ContainerPort)
		}
		return fmt.Sprintf(`# syntax=docker/dockerfile:1.7
FROM node:%s-alpine
WORKDIR /app
COPY package*.json ./
RUN if [ -f package-lock.json ]; then npm ci --omit=dev --ignore-scripts --no-audit --no-fund; else npm install --omit=dev --ignore-scripts --no-audit --no-fund; fi
COPY . .
USER node
EXPOSE %d
CMD ["npm", "start"]
`, runtimeVersion, service.ContainerPort)
	default:
		if runtimeVersion == "" {
			runtimeVersion = "1.24"
		}
		return fmt.Sprintf(`# syntax=docker/dockerfile:1.7
FROM golang:%s-bookworm AS build
WORKDIR /src
COPY . .
ARG SOURCE_GIT_USER
ARG SOURCE_GIT_TOKEN
ARG GOPRIVATE
ENV GOPRIVATE=${GOPRIVATE}
RUN set -eu; \
    printf '#!/bin/sh\ncase "$1" in *Username*) printf "%%%%s\\n" "$SOURCE_GIT_USER";; *) printf "%%%%s\\n" "$SOURCE_GIT_TOKEN";; esac\n' > /tmp/git-askpass; \
    chmod 700 /tmp/git-askpass; \
    trap 'rm -f /tmp/git-askpass' EXIT; \
    export GIT_ASKPASS=/tmp/git-askpass GIT_TERMINAL_PROMPT=0; \
    CGO_ENABLED=0 GOOS=linux go build -buildvcs=false -trimpath -ldflags="-s -w" -o /out/app .

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/app /app/app
USER 65532:65532
EXPOSE %d
ENTRYPOINT ["/app/app"]
`, runtimeVersion, service.ContainerPort)
	}
}

func renderManifest(project, environment string, service ServiceSpec) string {
	resources := make([]string, 0, 4)
	if service.WorkloadType == "backend" && service.EtcdConfigEnabled {
		resources = append(resources, renderEtcdSecret(service))
	}
	resources = append(resources, renderDeployment(project, environment, service), renderKubernetesService(project, environment, service))
	return strings.Join(resources, "---\n")
}

func renderEtcdSecret(service ServiceSpec) string {
	var hosts strings.Builder
	for _, host := range service.EtcdHosts {
		fmt.Fprintf(&hosts, "        - %s\n", yamlScalar(host))
	}
	return fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: %s-etcd
  namespace: %s
type: Opaque
stringData:
  %s: |
    EtcdConf:
      Hosts:
%s      Key: %s
      User: %s
      Pass: {{ETCD_PASSWORD}}
`, service.Key, service.Namespace, service.EtcdConfigFile, hosts.String(), yamlScalar(service.EtcdConfigKey), yamlScalar(service.EtcdUsername))
}

func renderDeployment(project, environment string, service ServiceSpec) string {
	var pullSecrets strings.Builder
	if len(service.ImagePullSecrets) > 0 {
		pullSecrets.WriteString("      imagePullSecrets:\n")
		for _, secret := range service.ImagePullSecrets {
			fmt.Fprintf(&pullSecrets, "        - name: %s\n", secret)
		}
	}

	var probe string
	if service.WorkloadType == "frontend" {
		probe = fmt.Sprintf(`          startupProbe:
            httpGet:
              path: %s
              port: http
            periodSeconds: 5
            timeoutSeconds: 3
            failureThreshold: 12
          readinessProbe:
            httpGet:
              path: %s
              port: http
            initialDelaySeconds: 5
            periodSeconds: 10
            timeoutSeconds: 3
            failureThreshold: 6
          livenessProbe:
            httpGet:
              path: %s
              port: http
            initialDelaySeconds: 15
            periodSeconds: 20
            timeoutSeconds: 3
            failureThreshold: 6
`, yamlScalar(service.HealthPath), yamlScalar(service.HealthPath), yamlScalar(service.HealthPath))
	} else {
		probe = `          startupProbe:
            tcpSocket:
              port: http
            periodSeconds: 10
            timeoutSeconds: 2
            failureThreshold: 30
          readinessProbe:
            tcpSocket:
              port: http
            initialDelaySeconds: 10
            periodSeconds: 10
          livenessProbe:
            tcpSocket:
              port: http
            initialDelaySeconds: 30
            periodSeconds: 20
`
	}

	var nodeSelector string
	if service.ManifestMode != "repository" && service.WorkloadClass != "" && service.WorkloadClass != "general" {
		pool := map[string]string{"gateway": "ingress-gateway", "application": "business-workload", "platform": "platform-ops"}[service.WorkloadClass]
		if pool == "" {
			pool = service.WorkloadClass
		}
		nodeSelector = fmt.Sprintf("      nodeSelector:\n        workload-class: %s\n        ops-deploy.io/pool: %s\n      tolerations:\n        - key: workload-class\n          operator: Equal\n          value: %s\n          effect: NoSchedule\n", yamlScalar(service.WorkloadClass), yamlScalar(pool), yamlScalar(service.WorkloadClass))
	}

	var environmentVariables strings.Builder
	environmentVariables.WriteString("            - name: TZ\n              value: " + yamlScalar(service.Timezone) + "\n")
	keys := make([]string, 0, len(service.EnvironmentVariables))
	for key := range service.EnvironmentVariables {
		if key != "TZ" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Fprintf(&environmentVariables, "            - name: %s\n              value: %s\n", key, yamlScalar(service.EnvironmentVariables[key]))
	}
	if timezone, ok := service.EnvironmentVariables["TZ"]; ok {
		environmentVariables.Reset()
		environmentVariables.WriteString("            - name: TZ\n              value: " + yamlScalar(timezone) + "\n")
		for _, key := range keys {
			fmt.Fprintf(&environmentVariables, "            - name: %s\n              value: %s\n", key, yamlScalar(service.EnvironmentVariables[key]))
		}
	}
	secretKeys := make([]string, 0, len(service.SecretEnvironmentVariables))
	for key := range service.SecretEnvironmentVariables {
		secretKeys = append(secretKeys, key)
	}
	sort.Strings(secretKeys)
	for _, key := range secretKeys {
		reference := service.SecretEnvironmentVariables[key]
		fmt.Fprintf(&environmentVariables, "            - name: %s\n              valueFrom:\n                secretKeyRef:\n                  name: %s\n                  key: %s\n", key, reference.SecretName, reference.SecretKey)
	}

	var javaCommandAndArgs strings.Builder
	javaOptions := normalizeJavaJVMOptions(service.JavaOptions)
	if service.Language == "java" && service.WorkloadType == "backend" && len(javaOptions) > 0 {
		javaCommandAndArgs.WriteString("          command:\n            - java\n          args:\n")
		for _, option := range javaOptions {
			option = strings.ReplaceAll(option, "{{environment}}", environment)
			// Stored deliveries created before validation was tightened must not be
			// able to inject extra YAML while being re-provisioned.
			lowerOption := strings.ToLower(option)
			if len(option) > 512 || strings.ContainsAny(option, "\x00\r\n\t") || strings.Contains(option, ": ") || strings.Contains(option, " #") || strings.HasSuffix(option, ":") || !strings.HasPrefix(option, "-") || lowerOption == "-jar" || lowerOption == "-cp" || lowerOption == "-classpath" || sensitiveJavaOptionPattern.MatchString(option) {
				continue
			}
			fmt.Fprintf(&javaCommandAndArgs, "            - %s\n", option)
		}
		javaCommandAndArgs.WriteString("            - -jar\n            - app.jar\n")
	}

	var volumeMounts, volumes string
	if service.WorkloadType == "backend" && service.EtcdConfigEnabled {
		volumeMounts = fmt.Sprintf(`          volumeMounts:
            - name: etcd-config
              mountPath: %s
              subPath: %s
`, yamlScalar(service.EtcdMountPath), service.EtcdConfigFile)
		volumes = fmt.Sprintf(`      volumes:
        - name: etcd-config
          secret:
            secretName: %s-etcd
`, service.Key)
	}

	return fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: %s
  namespace: %s
  labels:
    app: %s
    ops-deploy.io/project: %s
    ops-deploy.io/environment: %s
spec:
  replicas: %d
  revisionHistoryLimit: %d
  selector:
    matchLabels:
      app: %s
  template:
    metadata:
      labels:
        app: %s
        ops-deploy.io/project: %s
        ops-deploy.io/environment: %s
    spec:
      automountServiceAccountToken: false
      securityContext:
        runAsNonRoot: true
        seccompProfile:
          type: RuntimeDefault
%s%s      containers:
        - name: app
          image: {{IMAGE}}
          imagePullPolicy: %s
          securityContext:
            allowPrivilegeEscalation: false
            capabilities:
              drop:
                - ALL
%s          env:
%s
          ports:
            - name: http
              containerPort: %d
              protocol: TCP
%s          resources:
            requests:
              cpu: %s
              memory: %s
            limits:
              cpu: %s
              memory: %s
%s%s`, service.Key, service.Namespace, service.Key, project, environment, service.Replicas, service.RevisionHistoryLimit, service.Key, service.Key, project, environment, nodeSelector, pullSecrets.String(), service.ImagePullPolicy, javaCommandAndArgs.String(), strings.TrimRight(environmentVariables.String(), "\n"), service.ContainerPort, probe, service.CPURequest, service.MemoryRequest, service.CPULimit, service.MemoryLimit, volumeMounts, volumes)
}

// normalizeJavaJVMOptions keeps only user-managed JVM options and removes the
// launcher suffix managed by the platform. It deliberately handles historical
// records containing the complete managed pair while leaving malformed partial
// input for normal validation to reject.
func normalizeJavaJVMOptions(options []string) []string {
	normalized := make([]string, 0, len(options))
	for index := 0; index < len(options); index++ {
		option := strings.TrimSpace(options[index])
		if option == "" {
			continue
		}
		if strings.EqualFold(option, "-jar") {
			if index+1 < len(options) && isManagedJavaJarTarget(options[index+1]) {
				index++
				continue
			}
		}
		normalized = append(normalized, option)
	}
	return normalized
}

func isManagedJavaJarTarget(value string) bool {
	value = strings.TrimSpace(value)
	return value == "app.jar" || value == "/app/app.jar"
}

func renderKubernetesService(project, environment string, service ServiceSpec) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Service
metadata:
  name: %s
  namespace: %s
  labels:
    app: %s
    ops-deploy.io/project: %s
    ops-deploy.io/environment: %s
spec:
  type: ClusterIP
  selector:
    app: %s
  ports:
    - name: http
      port: %d
      targetPort: http
      protocol: TCP
`, service.Key, service.Namespace, service.Key, project, environment, service.Key, service.ContainerPort)
}

func groovyString(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "'", "\\'")
	value = strings.ReplaceAll(value, "$", "\\$")
	value = strings.ReplaceAll(value, "\r", "\\r")
	value = strings.ReplaceAll(value, "\n", "\\n")
	value = strings.ReplaceAll(value, "\u2028", "\\u2028")
	value = strings.ReplaceAll(value, "\u2029", "\\u2029")
	return "'" + value + "'"
}

func yamlScalar(value string) string {
	payload, _ := json.Marshal(value)
	return string(payload)
}
