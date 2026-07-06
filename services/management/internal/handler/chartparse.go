// Package handler — chartparse.go
//
// Pure parsing helpers for the Helm chart detail endpoint (FUT-022): they turn
// raw manifest / config / content bytes into structured chart data with no I/O,
// so they unit-test without any gRPC or HTTP fake. handleGetChart (chart.go)
// fetches the bytes and calls these.
package handler

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"path"
	"strings"
)

// Helm-on-OCI media types.
const (
	helmConfigMediaType  = "application/vnd.cncf.helm.config.v1+json"
	helmContentMediaType = "application/vnd.cncf.helm.chart.content.v1.tar+gzip"
)

// Size caps (spec §5).
const (
	configBlobCap  = 1 << 20   // 1 MiB — Chart.yaml config blob
	contentBlobCap = 10 << 20  // 10 MiB — chart .tgz content layer
	valuesCap      = 256 << 10 // 256 KiB — extracted values.yaml
	maxTarEntries  = 2000      // tar entries scanned before giving up
)

// ChartMetadata is the Chart.yaml view returned to the frontend. JSON tags are
// snake_case to match the BFF wire contract; omitempty on the optional fields.
type ChartMetadata struct {
	Name         string            `json:"name"`
	Version      string            `json:"version"`
	AppVersion   string            `json:"app_version,omitempty"`
	Description  string            `json:"description,omitempty"`
	APIVersion   string            `json:"api_version,omitempty"`
	Type         string            `json:"type,omitempty"`
	KubeVersion  string            `json:"kube_version,omitempty"`
	Home         string            `json:"home,omitempty"`
	Icon         string            `json:"icon,omitempty"`
	Deprecated   bool              `json:"deprecated,omitempty"`
	Keywords     []string          `json:"keywords,omitempty"`
	Sources      []string          `json:"sources,omitempty"`
	Maintainers  []ChartMaintainer `json:"maintainers,omitempty"`
	Dependencies []ChartDependency `json:"dependencies,omitempty"`
	Annotations  map[string]string `json:"annotations,omitempty"`
}

// ChartMaintainer is one Chart.yaml maintainer entry.
type ChartMaintainer struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email,omitempty"`
	URL   string `json:"url,omitempty"`
}

// ChartDependency is one Chart.yaml dependency entry.
type ChartDependency struct {
	Name       string `json:"name,omitempty"`
	Version    string `json:"version,omitempty"`
	Repository string `json:"repository,omitempty"`
}

// ociDescriptor is the subset of an OCI content descriptor we read.
type ociDescriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
}

// ociManifest is the subset of an OCI image manifest we read.
type ociManifest struct {
	Config ociDescriptor   `json:"config"`
	Layers []ociDescriptor `json:"layers"`
}

// parseManifestConfigAndLayer reads the manifest JSON and returns the config
// digest + config mediaType + the Helm content-layer digest. It does NOT error
// on a non-Helm manifest — the caller inspects the returned mediaType and
// decides. contentDigest is empty when no Helm content layer is present.
func parseManifestConfigAndLayer(raw []byte) (configDigest, configMediaType, contentDigest string, err error) {
	var m ociManifest
	if err = json.Unmarshal(raw, &m); err != nil {
		return "", "", "", err
	}
	for _, l := range m.Layers {
		if l.MediaType == helmContentMediaType {
			contentDigest = l.Digest
			break
		}
	}
	return m.Config.Digest, m.Config.MediaType, contentDigest, nil
}

// helmConfig mirrors the camelCase JSON of a Helm config blob (Chart.yaml).
type helmConfig struct {
	Name        string            `json:"name"`
	Version     string            `json:"version"`
	AppVersion  string            `json:"appVersion"`
	Description string            `json:"description"`
	APIVersion  string            `json:"apiVersion"`
	Type        string            `json:"type"`
	KubeVersion string            `json:"kubeVersion"`
	Home        string            `json:"home"`
	Icon        string            `json:"icon"`
	Deprecated  bool              `json:"deprecated"`
	Keywords    []string          `json:"keywords"`
	Sources     []string          `json:"sources"`
	Maintainers []struct {
		Name  string `json:"name"`
		Email string `json:"email"`
		URL   string `json:"url"`
	} `json:"maintainers"`
	Dependencies []struct {
		Name       string `json:"name"`
		Version    string `json:"version"`
		Repository string `json:"repository"`
	} `json:"dependencies"`
	Annotations map[string]string `json:"annotations"`
}

// parseChartMetadata unmarshals a Helm config blob into the snake_case
// ChartMetadata returned to the FE.
func parseChartMetadata(configJSON []byte) (ChartMetadata, error) {
	var c helmConfig
	if err := json.Unmarshal(configJSON, &c); err != nil {
		return ChartMetadata{}, err
	}
	m := ChartMetadata{
		Name: c.Name, Version: c.Version, AppVersion: c.AppVersion,
		Description: c.Description, APIVersion: c.APIVersion, Type: c.Type,
		KubeVersion: c.KubeVersion, Home: c.Home, Icon: c.Icon,
		Deprecated: c.Deprecated, Keywords: c.Keywords, Sources: c.Sources,
		Annotations: c.Annotations,
	}
	for _, mt := range c.Maintainers {
		m.Maintainers = append(m.Maintainers, ChartMaintainer{Name: mt.Name, Email: mt.Email, URL: mt.URL})
	}
	for _, d := range c.Dependencies {
		m.Dependencies = append(m.Dependencies, ChartDependency{Name: d.Name, Version: d.Version, Repository: d.Repository})
	}
	return m, nil
}

// errValuesNotFound is returned when no chart-root values.yaml is in the archive.
var errValuesNotFound = errors.New("values.yaml not found in chart archive")

// extractValuesYAML gunzips + untars a chart .tgz and returns the chart-root
// values.yaml (a path shaped "<single-segment>/values.yaml"). It ignores
// subchart values, directory-traversal paths, and non-regular entries; caps
// the returned string at `limit` bytes (truncated=true when it hit the cap);
// and wraps the tar reader in an io.LimitReader so a lying header can't blow
// memory. (`limit`, not `cap`, to avoid shadowing the builtin.)
func extractValuesYAML(tgz []byte, limit int) (values string, truncated bool, err error) {
	gr, err := gzip.NewReader(bytes.NewReader(tgz))
	if err != nil {
		return "", false, err
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	for i := 0; i < maxTarEntries; i++ {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", false, err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		clean := path.Clean(hdr.Name)
		if strings.HasPrefix(clean, "..") || strings.HasPrefix(clean, "/") {
			continue // reject traversal / absolute paths
		}
		// Chart-root values.yaml only: exactly "<name>/values.yaml".
		parts := strings.Split(clean, "/")
		if len(parts) != 2 || parts[1] != "values.yaml" {
			continue
		}
		lr := io.LimitReader(tr, int64(limit)+1)
		buf, err := io.ReadAll(lr)
		if err != nil {
			return "", false, err
		}
		if len(buf) > limit {
			return string(buf[:limit]), true, nil
		}
		return string(buf), false, nil
	}
	return "", false, errValuesNotFound
}
