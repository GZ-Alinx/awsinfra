package httpapi

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/GZ-Alinx/awsinfra/internal/auth"
	"github.com/GZ-Alinx/awsinfra/internal/statebackend"
)

type terraformStateLocation struct {
	Project          string `json:"project"`
	Environment      string `json:"environment"`
	Stage            string `json:"stage"`
	Backend          string `json:"backend"`
	Bucket           string `json:"bucket"`
	Region           string `json:"region"`
	ObjectKey        string `json:"object_key"`
	Lineage          string `json:"lineage"`
	Serial           int64  `json:"serial"`
	ManagedResources int    `json:"managed_resources"`
	UpdatedAt        string `json:"updated_at"`
}

func (s *Server) getTerraformStateCenter(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformPermission(w, r, platformManageCredentials) {
		return
	}
	if s.stateBackend == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("Terraform state center service is unavailable"))
		return
	}
	info, err := s.stateBackend.Info(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	states, err := readTerraformStateLocations(s.config.Paths.DataDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"config": info, "states": states})
}

func (s *Server) saveTerraformStateCenter(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformPermission(w, r, platformManageCredentials) {
		return
	}
	if s.stateBackend == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("Terraform state center service is unavailable"))
		return
	}
	var request struct {
		statebackend.Input
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	current, currentErr := s.stateBackend.Info(r.Context())
	if currentErr != nil {
		writeError(w, http.StatusInternalServerError, currentErr)
		return
	}
	if current.Configured && (current.Bucket != strings.TrimSpace(strings.ToLower(request.Bucket)) || current.Region != strings.TrimSpace(strings.ToLower(request.Region)) || current.KeyPrefix != strings.Trim(strings.TrimSpace(request.KeyPrefix), "/")) {
		states, listErr := readTerraformStateLocations(s.config.Paths.DataDir)
		if listErr != nil {
			writeError(w, http.StatusInternalServerError, listErr)
			return
		}
		for _, item := range states {
			if item.Bucket == current.Bucket && strings.HasPrefix(item.ObjectKey, strings.Trim(current.KeyPrefix, "/")+"/projects/") {
				request.SecretAccessKey, request.SessionToken, request.Password = "", "", ""
				writeError(w, http.StatusConflict, errors.New("状态中心已经保存项目 state，不能直接修改 bucket、Region 或路径前缀；请先执行受控迁移"))
				return
			}
		}
	}
	session, _ := auth.SessionFromContext(r.Context())
	if strings.TrimSpace(request.Password) != "" {
		if err := s.authentication.ReauthenticateRequest(r, session.Username, request.Password); err != nil {
			request.SecretAccessKey, request.SessionToken, request.Password = "", "", ""
			if errors.Is(err, auth.ErrRateLimited) {
				writeError(w, http.StatusTooManyRequests, err)
				return
			}
			writeError(w, http.StatusUnauthorized, auth.ErrInvalidCredentials)
			return
		}
	}
	info, err := s.stateBackend.SaveAndVerify(r.Context(), session.Username, request.Input)
	request.SecretAccessKey, request.SessionToken, request.Password = "", "", ""
	if errors.Is(err, statebackend.ErrInvalidConfig) {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func readTerraformStateLocations(dataDir string) ([]terraformStateLocation, error) {
	root := filepath.Join(dataDir, "state-metadata")
	result := make([]terraformStateLocation, 0)
	err := filepath.WalkDir(root, func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			return nil
		}
		payload, err := os.ReadFile(filePath) // #nosec G304 -- path is discovered below the platform-owned metadata root.
		if err != nil {
			return err
		}
		var item terraformStateLocation
		if err := json.Unmarshal(payload, &item); err != nil {
			return err
		}
		result = append(result, item)
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	sort.Slice(result, func(i, j int) bool {
		left := result[i].Project + "/" + result[i].Environment + "/" + result[i].Stage
		right := result[j].Project + "/" + result[j].Environment + "/" + result[j].Stage
		return left < right
	})
	return result, nil
}
