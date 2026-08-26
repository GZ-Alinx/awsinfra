package cicd

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestJenkinsReferencePathSurvivesTunnelReconnect(t *testing.T) {
	client, err := newJenkinsClient("http://127.0.0.1:45002", "admin", "token")
	if err != nil {
		t.Fatal(err)
	}
	path, err := client.referencePath("http://127.0.0.1:43925/queue/item/42/")
	if err != nil || path != "/queue/item/42/" {
		t.Fatalf("referencePath() = %q, %v", path, err)
	}
	if _, err := client.referencePath("file:///etc/passwd"); err == nil {
		t.Fatal("unsupported Jenkins URL scheme was accepted")
	}
	path, err = client.referencePath("queue/item/43/")
	if err != nil || path != "/queue/item/43/" {
		t.Fatalf("relative referencePath() = %q, %v", path, err)
	}
}

func TestJenkinsHTTPErrorIsClassifiedAndSanitized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`<html><head><script>secretCrumb = "must-not-leak"</script></head><body><h1>Error 500</h1><pre>Illegal choice: platform-order,platform-external</pre></body></html>`))
	}))
	defer server.Close()
	client, err := newJenkinsClient(server.URL, "admin", "token")
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.request(context.Background(), http.MethodGet, "/", nil, "")
	if !errors.Is(err, ErrJenkins) {
		t.Fatalf("Jenkins error was not classified: %v", err)
	}
	if got := err.Error(); !strings.Contains(got, "Illegal choice") || strings.Contains(got, "must-not-leak") || strings.Contains(got, "<html") {
		t.Fatalf("unsafe or unhelpful Jenkins error: %s", got)
	}
}

func TestJenkinsRefreshRecoversExpiredQueueItemFromBuildHistory(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/queue/item/62/api/json":
			http.NotFound(w, r)
		case "/job/demo/api/json":
			if tree := r.URL.Query().Get("tree"); !strings.Contains(tree, "queueId") {
				t.Fatalf("build history query does not request queueId: %q", tree)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"builds":[{"number":7,"url":"` + requestServerURL(r) + `/job/demo/7/","queueId":62}]}`))
		case "/job/demo/7/api/json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"building":false,"result":"SUCCESS","url":"` + requestServerURL(r) + `/job/demo/7/","timestamp":1000,"duration":2000}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := newJenkinsClient(server.URL, "admin", "token")
	if err != nil {
		t.Fatal(err)
	}
	build, err := client.refresh(context.Background(), "demo", Build{QueueURL: "/queue/item/62/", Status: "queued", Progress: 5, CreatedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if build.BuildNumber != 7 || build.BuildURL != "/job/demo/7/" || build.Status != "succeeded" || build.Result != "SUCCESS" || build.Progress != 100 {
		t.Fatalf("expired queue recovery returned %#v", build)
	}
}

func TestJenkinsTriggerRecoversCoalescedQueueItemWithoutLocation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/crumbIssuer/api/json":
			http.NotFound(w, r)
		case "/job/demo/buildWithParameters":
			w.WriteHeader(http.StatusCreated)
		case "/queue/api/json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"items":[{"id":77,"url":"` + requestServerURL(r) + `/queue/item/77/","task":{"name":"demo","url":"` + requestServerURL(r) + `/job/demo/"},"actions":[{"parameters":[{"name":"TARGET_SERVICES","value":"aviator"},{"name":"GIT_BRANCH","value":"test"}]}]}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := newJenkinsClient(server.URL, "admin", "token")
	if err != nil {
		t.Fatal(err)
	}
	queueURL, err := client.trigger(context.Background(), "demo", map[string]string{"TARGET_SERVICES": "aviator", "GIT_BRANCH": "test"})
	if err != nil {
		t.Fatal(err)
	}
	if queueURL != "/queue/item/77/" {
		t.Fatalf("recovered queue URL = %q", queueURL)
	}
}

func requestServerURL(r *http.Request) string {
	return "http://" + r.Host
}

func TestInlineJobReusesCrumbSessionCookie(t *testing.T) {
	const (
		crumb  = "test-crumb"
		cookie = "crumb-session"
	)
	created := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/job/demo/config.xml":
			http.NotFound(w, r)
		case "/crumbIssuer/api/json":
			http.SetCookie(w, &http.Cookie{Name: "JSESSIONID", Value: cookie, Path: "/"})
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"crumbRequestField":"Jenkins-Crumb","crumb":"` + crumb + `"}`))
		case "/createItem":
			session, err := r.Cookie("JSESSIONID")
			if err != nil || session.Value != cookie || r.Header.Get("Jenkins-Crumb") != crumb || r.Header.Get("Content-Type") != "application/xml; charset=utf-8" {
				http.Error(w, "No valid crumb was included in the request", http.StatusForbidden)
				return
			}
			created = true
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := newJenkinsClient(server.URL, "admin", "token")
	if err != nil {
		t.Fatal(err)
	}
	job := Job{JenkinsJobName: "demo", DisplayName: "demo", Enabled: true, ServiceName: "demo", ServiceKeys: []string{"demo"}, Language: "go", JenkinsfileMode: "existing", ManifestRepo: "https://example.invalid/manifest.git", ManifestBranch: "main", ManifestPath: "manifest.yaml"}
	if err := client.upsertInlineJob(context.Background(), job, "pipeline { agent none }"); err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("expected Jenkins Job to be created")
	}
}
