/*
 *     Copyright 2026 The Dragonfly Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *      http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package target

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/dragonflyoss/perf-tests/tools/proxy-bench/fixed-pool/internal/contract"
	"github.com/dragonflyoss/perf-tests/tools/proxy-bench/fixed-pool/internal/dataset"
)

const taskHeader = "X-Dragonfly-Task-ID"
const serverHeader = "X-Dragonfly-Server-IP"
const finishedHeader = "X-Dragonfly-Task-Download-Finished"

// FixedHeaders is shared with later execution input generation; explicit proxy
// selection and a fixed identity prevent environment-dependent bypasses.
func FixedHeaders() http.Header {
	return http.Header{
		"X-Dragonfly-Use-P2p":                          []string{headerTrue},
		"X-Dragonfly-Enable-Task-Id-Based-Blob-Digest": []string{headerFalse},
		"X-Dragonfly-Prefetch":                         []string{headerFalse},
		"Accept-Encoding":                              []string{"identity"},
	}
}

func newClient(c Config) (*http.Client, *http.Transport, error) {
	proxy, err := url.Parse(c.Target.Proxy)
	if err != nil {
		return nil, nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyURL(proxy)
	transport.DisableCompression = true
	// Avoid implicit transport retries on reused connections: every probe is issued once.
	transport.DisableKeepAlives = true
	client := &http.Client{Transport: transport, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	return client, transport, nil
}

func request(ctx context.Context, client *http.Client, c Config, o contract.Object, rangeRequest bool) (*http.Response, context.CancelFunc, error) {
	ms := c.Prepare.RequestTimeoutMS
	if rangeRequest {
		ms = c.Prepare.ReadyTimeoutMS
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(ms)*time.Millisecond)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.URL, nil)
	if err != nil {
		cancel()
		return nil, func() {}, err
	}
	req.Header = FixedHeaders()
	if rangeRequest {
		req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", min(uint64(65536), o.Size)-1))
	}
	resp, err := client.Do(req)
	return resp, cancel, err
}

func responseIdentity(resp *http.Response, c Config) (string, string, error) {
	task, server := resp.Header.Get(taskHeader), resp.Header.Get(serverHeader)
	if len(resp.Header.Values(taskHeader)) != 1 || task == "" || len(resp.Header.Values(serverHeader)) != 1 || net.ParseIP(server) == nil {
		return task, server, contract.Tag(Unsupported, fmt.Errorf("HTTP %d: missing/ambiguous Dragonfly task or server IP headers", resp.StatusCode))
	}
	if !net.ParseIP(server).Equal(net.ParseIP(c.Target.ExpectedServerIP)) {
		return task, server, contract.Tag(contract.PrepareFailed, fmt.Errorf("HTTP %d: server IP %q differs from expected %q", resp.StatusCode, server, c.Target.ExpectedServerIP))
	}
	if encoding := resp.Header.Get("Content-Encoding"); encoding != "" && encoding != "identity" {
		return task, server, contract.Tag(contract.PrepareFailed, fmt.Errorf("HTTP %d: unexpected Content-Encoding %q", resp.StatusCode, encoding))
	}
	return task, server, nil
}

func fullRead(ctx context.Context, client *http.Client, c Config, o contract.Object, r *ObjectResult) error {
	resp, cancel, err := request(ctx, client, c, o, false)
	defer cancel()
	if err != nil {
		return contract.Tag(contract.PrepareFailed, fmt.Errorf("full GET: %w", err))
	}
	// The response body is always closed, including failures before body reads.
	err = readFullResponse(ctx, resp, c, o, r)
	closeErr := resp.Body.Close()
	if err == nil {
		err = closeErr
	}
	return err
}

func readFullResponse(ctx context.Context, resp *http.Response, c Config, o contract.Object, r *ObjectResult) error {
	if resp.StatusCode != http.StatusOK {
		return contract.Tag(contract.PrepareFailed, fmt.Errorf("full GET requires HTTP 200, got %d", resp.StatusCode))
	}
	task, server, err := responseIdentity(resp, c)
	if task != "" {
		r.TaskID = &task
	}
	if server != "" {
		r.ServerIP = &server
	}
	if err != nil {
		return err
	}
	hash := sha256.New()
	n, err := io.CopyBuffer(hash, io.LimitReader(resp.Body, int64(o.Size)+1), make([]byte, 64*1024))
	count := uint64(n)
	r.ReceivedBytes = &count
	digest := hex.EncodeToString(hash.Sum(nil))
	r.SHA256 = &digest
	if err != nil {
		return contract.Tag(contract.PrepareFailed, fmt.Errorf("HTTP %d full GET after %d bytes: %w", resp.StatusCode, n, err))
	}
	if count != o.Size || digest != o.SHA256 {
		return contract.Tag(contract.PrepareFailed, fmt.Errorf("HTTP %d full GET body mismatch: received %d, expected %d bytes; SHA-256 %s", resp.StatusCode, count, o.Size, digest))
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	now := timestamp()
	r.FullReadCompletedAt = &now
	return nil
}

func finishedCheck(ctx context.Context, client *http.Client, c Config, o contract.Object, r *ObjectResult) error {
	resp, cancel, err := request(ctx, client, c, o, true)
	defer cancel()
	if err != nil {
		return contract.Tag(Unverified, fmt.Errorf("finished check: %w", err))
	}
	err = readProbe(ctx, resp, c, o, r)
	closeErr := resp.Body.Close()
	if err == nil {
		err = closeErr
	}
	return err
}

func readProbe(ctx context.Context, resp *http.Response, c Config, o contract.Object, r *ObjectResult) error {
	if resp.StatusCode != http.StatusPartialContent {
		return contract.Tag(contract.PrepareFailed, fmt.Errorf("finished check requires HTTP 206, got %d", resp.StatusCode))
	}
	task, _, err := responseIdentity(resp, c)
	if err != nil {
		return err
	}
	if r.TaskID == nil || task != *r.TaskID {
		return contract.Tag(contract.PrepareFailed, fmt.Errorf("HTTP %d: task identity changed during finished check", resp.StatusCode))
	}
	size := min(uint64(65536), o.Size)
	want := "bytes 0-" + strconv.FormatUint(size-1, 10) + "/" + strconv.FormatUint(o.Size, 10)
	if len(resp.Header.Values("Content-Range")) != 1 || resp.Header.Get("Content-Range") != want || resp.ContentLength != int64(size) {
		return contract.Tag(contract.PrepareFailed, fmt.Errorf("HTTP %d finished check Content-Range/length mismatch: %q, %d", resp.StatusCode, resp.Header.Get("Content-Range"), resp.ContentLength))
	}
	// This header describes task state at request start, not response completion.
	if len(resp.Header.Values(finishedHeader)) != 1 || resp.Header.Get(finishedHeader) != headerTrue {
		return contract.Tag(Unverified, fmt.Errorf("HTTP %d finished check has no unambiguous true completion marker; no retry or repair attempted", resp.StatusCode))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(size)+1))
	if err != nil {
		return contract.Tag(Unverified, err)
	}
	var expected bytes.Buffer
	if err := dataset.Generate(ctx, &expected, o.ID, size); err != nil {
		return err
	}
	if !bytes.Equal(body, expected.Bytes()) {
		return contract.Tag(contract.PrepareFailed, fmt.Errorf("HTTP %d finished check deterministic bytes mismatch", resp.StatusCode))
	}
	now := timestamp()
	r.FinishedCheckAt = &now
	return nil
}

func timestamp() string { return time.Now().UTC().Format(time.RFC3339Nano) }
