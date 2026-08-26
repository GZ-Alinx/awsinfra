package httpapi

import (
	"errors"
	"net/http"
	"os"
	"strings"

	"github.com/GZ-Alinx/awsinfra/internal/auth"
	"github.com/GZ-Alinx/awsinfra/internal/awscredentials"
	"github.com/GZ-Alinx/awsinfra/internal/staticcdn"
)

const maxStaticCDNProxyUploadBytes int64 = 100 << 20

func (s *Server) listStaticCDNs(w http.ResponseWriter, r *http.Request) {
	project, environment := r.PathValue("project"), r.PathValue("environment")
	if _, err := s.requireProjectView(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	if _, err := s.accessControl.Environment(r.Context(), project, environment); err != nil {
		writeAccessError(w, err)
		return
	}
	if s.staticCDN == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("静态资源 CDN 服务不可用"))
		return
	}
	refresh := r.URL.Query().Get("fresh") == "true"
	if refresh {
		if err := s.requireBoundProjectAWSCredential(r.Context(), project); err != nil {
			writeProjectAWSCredentialError(w, err)
			return
		}
	}
	items, err := s.staticCDN.List(r.Context(), project, environment, refresh)
	if err != nil {
		writeStaticCDNError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) createStaticCDN(w http.ResponseWriter, r *http.Request) {
	project, environment := r.PathValue("project"), r.PathValue("environment")
	if err := s.requireProjectConfigure(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	item, err := s.accessControl.Environment(r.Context(), project, environment)
	if err != nil {
		writeAccessError(w, err)
		return
	}
	if err := s.requireBoundProjectAWSCredential(r.Context(), project); err != nil {
		writeProjectAWSCredentialError(w, err)
		return
	}
	if s.staticCDN == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("静态资源 CDN 服务不可用"))
		return
	}
	var request struct {
		DisplayName string   `json:"display_name"`
		BucketName  string   `json:"bucket_name"`
		CORSOrigins []string `json:"cors_origins"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	session, _ := auth.SessionFromContext(r.Context())
	resource, err := s.staticCDN.Create(r.Context(), staticcdn.Resource{
		ProjectKey: project, EnvironmentKey: environment, DisplayName: request.DisplayName,
		BucketName: request.BucketName, Region: item.Region, CORSOrigins: request.CORSOrigins,
	}, session.Username)
	if err != nil {
		writeStaticCDNError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, resource)
}

func (s *Server) refreshStaticCDN(w http.ResponseWriter, r *http.Request) {
	project, environment, bucket := r.PathValue("project"), r.PathValue("environment"), r.PathValue("bucket")
	if _, err := s.requireProjectView(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	if _, err := s.accessControl.Environment(r.Context(), project, environment); err != nil {
		writeAccessError(w, err)
		return
	}
	if err := s.requireBoundProjectAWSCredential(r.Context(), project); err != nil {
		writeProjectAWSCredentialError(w, err)
		return
	}
	resource, err := s.staticCDN.Refresh(r.Context(), project, environment, bucket)
	if err != nil {
		writeStaticCDNError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resource)
}

func (s *Server) listStaticCDNObjects(w http.ResponseWriter, r *http.Request) {
	project, environment, bucket := r.PathValue("project"), r.PathValue("environment"), r.PathValue("bucket")
	if _, err := s.requireProjectView(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	if _, err := s.accessControl.Environment(r.Context(), project, environment); err != nil {
		writeAccessError(w, err)
		return
	}
	if err := s.requireBoundProjectAWSCredential(r.Context(), project); err != nil {
		writeProjectAWSCredentialError(w, err)
		return
	}
	items, err := s.staticCDN.Objects(r.Context(), project, environment, bucket, r.URL.Query().Get("prefix"))
	if err != nil {
		writeStaticCDNError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) authorizeStaticCDNUpload(w http.ResponseWriter, r *http.Request) {
	project, environment, bucket := r.PathValue("project"), r.PathValue("environment"), r.PathValue("bucket")
	if err := s.requireProjectConfigure(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	if _, err := s.accessControl.Environment(r.Context(), project, environment); err != nil {
		writeAccessError(w, err)
		return
	}
	if err := s.requireBoundProjectAWSCredential(r.Context(), project); err != nil {
		writeProjectAWSCredentialError(w, err)
		return
	}
	var request struct {
		Key string `json:"key"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	authorization, err := s.staticCDN.AuthorizeUpload(r.Context(), project, environment, bucket, request.Key)
	if err != nil {
		writeStaticCDNError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, authorization)
}

func (s *Server) uploadStaticCDNObject(w http.ResponseWriter, r *http.Request) {
	project, environment, bucket := r.PathValue("project"), r.PathValue("environment"), r.PathValue("bucket")
	if err := s.requireProjectConfigure(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	if _, err := s.accessControl.Environment(r.Context(), project, environment); err != nil {
		writeAccessError(w, err)
		return
	}
	if err := s.requireBoundProjectAWSCredential(r.Context(), project); err != nil {
		writeProjectAWSCredentialError(w, err)
		return
	}
	if s.staticCDN == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("静态资源 CDN 服务不可用"))
		return
	}
	if r.ContentLength > maxStaticCDNProxyUploadBytes {
		writeError(w, http.StatusRequestEntityTooLarge, errors.New("中转上传文件不能超过 100 MiB"))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxStaticCDNProxyUploadBytes)
	if err := s.staticCDN.UploadObject(r.Context(), project, environment, bucket, r.PathValue("key"), r.Header.Get("Content-Type"), r.Body); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, errors.New("中转上传文件不能超过 100 MiB"))
			return
		}
		writeStaticCDNError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteStaticCDNObject(w http.ResponseWriter, r *http.Request) {
	project, environment, bucket := r.PathValue("project"), r.PathValue("environment"), r.PathValue("bucket")
	if err := s.requireProjectConfigure(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	if _, err := s.accessControl.Environment(r.Context(), project, environment); err != nil {
		writeAccessError(w, err)
		return
	}
	if err := s.requireBoundProjectAWSCredential(r.Context(), project); err != nil {
		writeProjectAWSCredentialError(w, err)
		return
	}
	if err := s.staticCDN.DeleteObject(r.Context(), project, environment, bucket, r.PathValue("key")); err != nil {
		writeStaticCDNError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) invalidateStaticCDN(w http.ResponseWriter, r *http.Request) {
	project, environment, bucket := r.PathValue("project"), r.PathValue("environment"), r.PathValue("bucket")
	if err := s.requireProjectConfigure(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	if _, err := s.accessControl.Environment(r.Context(), project, environment); err != nil {
		writeAccessError(w, err)
		return
	}
	if err := s.requireBoundProjectAWSCredential(r.Context(), project); err != nil {
		writeProjectAWSCredentialError(w, err)
		return
	}
	var request struct {
		Paths []string `json:"paths"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.staticCDN.Invalidate(r.Context(), project, environment, bucket, request.Paths); err != nil {
		writeStaticCDNError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "in_progress"})
}

func (s *Server) deleteStaticCDN(w http.ResponseWriter, r *http.Request) {
	project, environment, bucket := r.PathValue("project"), r.PathValue("environment"), r.PathValue("bucket")
	if err := s.requireProjectConfigure(r, project); err != nil {
		writeAccessError(w, err)
		return
	}
	if _, err := s.accessControl.Environment(r.Context(), project, environment); err != nil {
		writeAccessError(w, err)
		return
	}
	if err := s.requireBoundProjectAWSCredential(r.Context(), project); err != nil {
		writeProjectAWSCredentialError(w, err)
		return
	}
	var request struct {
		Confirm string `json:"confirm"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(request.Confirm) != bucket {
		writeError(w, http.StatusBadRequest, errors.New("请输入完整 S3 Bucket 名称确认删除"))
		return
	}
	resource, err := s.staticCDN.Delete(r.Context(), project, environment, bucket)
	if errors.Is(err, staticcdn.ErrDeletePending) {
		writeJSON(w, http.StatusAccepted, map[string]any{"resource": resource, "message": err.Error()})
		return
	}
	if err != nil {
		writeStaticCDNError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resource)
}

func writeStaticCDNError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, staticcdn.ErrInvalid):
		writeError(w, http.StatusBadRequest, err)
	case errors.Is(err, staticcdn.ErrNotFound), errors.Is(err, os.ErrNotExist):
		writeError(w, http.StatusNotFound, staticcdn.ErrNotFound)
	case errors.Is(err, staticcdn.ErrConflict), errors.Is(err, staticcdn.ErrBucketNotEmpty):
		writeError(w, http.StatusConflict, err)
	case errors.Is(err, awscredentials.ErrCredentialNotBound), errors.Is(err, awscredentials.ErrCredentialMismatch):
		writeProjectAWSCredentialError(w, err)
	case strings.Contains(strings.ToLower(err.Error()), "accessdenied"),
		strings.Contains(strings.ToLower(err.Error()), "unauthorizedoperation"):
		writeError(w, http.StatusFailedDependency, errors.New("项目关联的 AWS 身份缺少 S3 或 CloudFront 权限，请补充对应权限后重试"))
	default:
		writeError(w, http.StatusBadGateway, err)
	}
}
