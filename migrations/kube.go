package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"
)

var errNotFound = errors.New("not found")
var errConflict = errors.New("conflict")

type kubeClient struct {
	base, tokenFile string
	client          *http.Client
}
type job struct {
	Metadata struct{ Annotations map[string]string }
	Status   struct {
		Conditions []struct{ Type, Status string }
	}
}

func inClusterClient() (*kubeClient, error) {
	const dir = "/var/run/secrets/kubernetes.io/serviceaccount/"
	ca, err := os.ReadFile(dir + "ca.crt")
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return nil, errors.New("invalid cluster CA")
	}
	return &kubeClient{base: "https://kubernetes.default.svc", tokenFile: dir + "token", client: &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}}}, nil
}
func (k *kubeClient) do(ctx context.Context, method, endpoint string, body any) ([]byte, error) {
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, k.base+endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if k.tokenFile != "" {
		token, err := os.ReadFile(k.tokenFile)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+string(token))
	}
	res, err := k.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode == 404 {
		return nil, errNotFound
	}
	if res.StatusCode == 409 {
		return nil, errConflict
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("cluster API %s %s: HTTP %d", method, endpoint, res.StatusCode)
	}
	return io.ReadAll(io.LimitReader(res.Body, 2<<20))
}
func jobsPath(ns string) string { return "/apis/batch/v1/namespaces/" + ns + "/jobs" }
func (k *kubeClient) getJob(ctx context.Context, ns, name string) (job, error) {
	var j job
	b, err := k.do(ctx, "GET", jobsPath(ns)+"/"+name, nil)
	if err != nil {
		return j, err
	}
	err = json.Unmarshal(b, &j)
	return j, err
}
func (k *kubeClient) createJob(ctx context.Context, ns string, j map[string]any) error {
	_, err := k.do(ctx, "POST", jobsPath(ns), j)
	return err
}
func (k *kubeClient) status(ctx context.Context, ns, name string) (map[string]any, error) {
	j, err := k.getJob(ctx, ns, name)
	if err != nil {
		return nil, err
	}
	state := "running"
	for _, c := range j.Status.Conditions {
		if c.Status == "True" {
			if c.Type == "Complete" {
				state = "succeeded"
			}
			if c.Type == "Failed" {
				state = "failed"
			}
		}
	}
	result := map[string]any{"job": name, "state": state, "audit": j.Metadata.Annotations}
	// Return logs only once terminal, bounded in length. Running/pending jobs
	// expose no partial success. Job failure includes pod eviction/deadlines.
	if state != "running" {
		endpoint := "/api/v1/namespaces/" + ns + "/pods"
		b, err := k.do(ctx, "GET", endpoint+"?labelSelector="+url.QueryEscape("job-name="+name), nil)
		if err != nil {
			return nil, err
		}
		var pods struct {
			Items []struct{ Metadata struct{ Name string } }
		}
		if err := json.Unmarshal(b, &pods); err != nil {
			return nil, err
		}
		logs := ""
		for _, p := range pods.Items {
			b, err := k.do(ctx, "GET", endpoint+"/"+url.PathEscape(p.Metadata.Name)+"/log?container=migration&tailLines=100&limitBytes=65536", nil)
			// A lost node or expired container log must not turn a failed job green.
			if err != nil {
				logs += "Container logs unavailable.\n"
			} else {
				logs += string(b)
			}
		}
		result["logs"] = logs
	}
	return result, nil
}
