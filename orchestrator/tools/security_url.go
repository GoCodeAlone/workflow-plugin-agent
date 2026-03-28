package tools

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/GoCodeAlone/workflow-plugin-agent/provider"
)

// SecurityScanURLTool checks a URL for common security issues.
type SecurityScanURLTool struct{}

func (t *SecurityScanURLTool) Name() string { return "security_scan_url" }
func (t *SecurityScanURLTool) Description() string {
	return "Scan a URL for security issues (TLS, headers, redirects)"
}
func (t *SecurityScanURLTool) Definition() provider.ToolDef {
	return provider.ToolDef{
		Name:        t.Name(),
		Description: "Scan a URL for common security issues: TLS certificate validity, security response headers (HSTS, CSP, X-Frame-Options, X-Content-Type-Options), HTTP→HTTPS redirect enforcement, and Server header exposure. Returns a score (0-100) and per-check findings.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url": map[string]any{
					"type":        "string",
					"description": "URL to scan (https:// recommended)",
				},
			},
			"required": []string{"url"},
		},
	}
}

type urlSecurityFinding struct {
	Check       string `json:"check"`
	Severity    string `json:"severity"`
	Status      string `json:"status"`
	Evidence    string `json:"evidence"`
	Remediation string `json:"remediation"`
}

func (t *SecurityScanURLTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	targetURL, _ := args["url"].(string)
	if targetURL == "" {
		return nil, fmt.Errorf("url is required")
	}

	var findings []urlSecurityFinding
	passed := 0
	failed := 0

	// 1. TLS Certificate Check
	finding := checkURLTLS(targetURL)
	findings = append(findings, finding)
	if finding.Status == "pass" {
		passed++
	} else {
		failed++
	}

	// 2. Security Headers Check
	headerFindings := checkURLSecurityHeaders(ctx, targetURL)
	for _, f := range headerFindings {
		findings = append(findings, f)
		if f.Status == "pass" {
			passed++
		} else {
			failed++
		}
	}

	// 3. HTTP→HTTPS Redirect Check
	finding = checkURLHTTPSRedirect(ctx, targetURL)
	findings = append(findings, finding)
	if finding.Status == "pass" {
		passed++
	} else {
		failed++
	}

	// 4. Server Header Exposure
	finding = checkURLServerHeader(ctx, targetURL)
	findings = append(findings, finding)
	if finding.Status == "pass" {
		passed++
	} else {
		failed++
	}

	total := passed + failed
	score := 0
	if total > 0 {
		score = (passed * 100) / total
	}

	// Convert findings to maps for JSON serialization
	findingMaps := make([]map[string]any, len(findings))
	for i, f := range findings {
		findingMaps[i] = map[string]any{
			"check":       f.Check,
			"severity":    f.Severity,
			"status":      f.Status,
			"evidence":    f.Evidence,
			"remediation": f.Remediation,
		}
	}

	return map[string]any{
		"url":      targetURL,
		"findings": findingMaps,
		"score":    score,
		"passed":   passed,
		"failed":   failed,
	}, nil
}

func checkURLTLS(targetURL string) urlSecurityFinding {
	host := extractURLHost(targetURL)
	if host == "" {
		return urlSecurityFinding{
			Check:       "tls_certificate",
			Severity:    "critical",
			Status:      "fail",
			Evidence:    "cannot parse host from URL",
			Remediation: "Provide a valid HTTPS URL",
		}
	}

	conn, err := tls.DialWithDialer(
		&net.Dialer{Timeout: 10 * time.Second},
		"tcp",
		host+":443",
		&tls.Config{},
	)
	if err != nil {
		return urlSecurityFinding{
			Check:       "tls_certificate",
			Severity:    "critical",
			Status:      "fail",
			Evidence:    fmt.Sprintf("TLS connection failed: %v", err),
			Remediation: "Install a valid TLS certificate",
		}
	}
	defer func() { _ = conn.Close() }()

	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return urlSecurityFinding{
			Check:       "tls_certificate",
			Severity:    "critical",
			Status:      "fail",
			Evidence:    "no certificates presented",
			Remediation: "Configure TLS certificates",
		}
	}

	cert := certs[0]
	daysUntilExpiry := time.Until(cert.NotAfter).Hours() / 24

	if daysUntilExpiry < 0 {
		return urlSecurityFinding{
			Check:       "tls_certificate",
			Severity:    "critical",
			Status:      "fail",
			Evidence:    fmt.Sprintf("certificate expired on %s", cert.NotAfter.Format("2006-01-02")),
			Remediation: "Renew TLS certificate immediately",
		}
	}
	if daysUntilExpiry < 30 {
		return urlSecurityFinding{
			Check:       "tls_certificate",
			Severity:    "warning",
			Status:      "fail",
			Evidence:    fmt.Sprintf("certificate expires in %.0f days (%s)", daysUntilExpiry, cert.NotAfter.Format("2006-01-02")),
			Remediation: "Renew TLS certificate soon",
		}
	}

	return urlSecurityFinding{
		Check:    "tls_certificate",
		Severity: "info",
		Status:   "pass",
		Evidence: fmt.Sprintf("valid until %s (%.0f days)", cert.NotAfter.Format("2006-01-02"), daysUntilExpiry),
	}
}

func checkURLSecurityHeaders(ctx context.Context, targetURL string) []urlSecurityFinding {
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return []urlSecurityFinding{{
			Check:    "security_headers",
			Severity: "warning",
			Status:   "fail",
			Evidence: fmt.Sprintf("request error: %v", err),
		}}
	}

	resp, err := client.Do(req)
	if err != nil {
		return []urlSecurityFinding{{
			Check:    "security_headers",
			Severity: "warning",
			Status:   "fail",
			Evidence: fmt.Sprintf("connection error: %v", err),
		}}
	}
	defer func() { _ = resp.Body.Close() }()

	type headerMeta struct {
		severity    string
		remediation string
	}
	headers := map[string]headerMeta{
		"Strict-Transport-Security": {
			severity:    "warning",
			remediation: "Add HSTS header: Strict-Transport-Security: max-age=31536000; includeSubDomains",
		},
		"Content-Security-Policy": {
			severity:    "warning",
			remediation: "Add CSP header to prevent XSS attacks",
		},
		"X-Frame-Options": {
			severity:    "info",
			remediation: "Add X-Frame-Options: DENY or SAMEORIGIN",
		},
		"X-Content-Type-Options": {
			severity:    "info",
			remediation: "Add X-Content-Type-Options: nosniff",
		},
	}

	var findings []urlSecurityFinding
	for header, meta := range headers {
		checkName := "header_" + strings.ToLower(strings.ReplaceAll(header, "-", "_"))
		val := resp.Header.Get(header)
		if val != "" {
			findings = append(findings, urlSecurityFinding{
				Check:    checkName,
				Severity: "info",
				Status:   "pass",
				Evidence: fmt.Sprintf("%s: %s", header, val),
			})
		} else {
			findings = append(findings, urlSecurityFinding{
				Check:       checkName,
				Severity:    meta.severity,
				Status:      "fail",
				Evidence:    fmt.Sprintf("missing %s header", header),
				Remediation: meta.remediation,
			})
		}
	}

	return findings
}

func checkURLHTTPSRedirect(ctx context.Context, targetURL string) urlSecurityFinding {
	httpURL := strings.Replace(targetURL, "https://", "http://", 1)
	if !strings.HasPrefix(httpURL, "http://") {
		httpURL = "http://" + extractURLHost(targetURL)
	}

	client := &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, httpURL, nil)
	if err != nil {
		return urlSecurityFinding{
			Check:    "https_redirect",
			Severity: "warning",
			Status:   "fail",
			Evidence: fmt.Sprintf("request error: %v", err),
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		// HTTP port not accessible is a good sign (nothing listening on plain HTTP)
		return urlSecurityFinding{
			Check:    "https_redirect",
			Severity: "info",
			Status:   "pass",
			Evidence: "HTTP port not accessible (good)",
		}
	}
	defer func() { _ = resp.Body.Close() }()

	location := resp.Header.Get("Location")
	if resp.StatusCode >= 300 && resp.StatusCode < 400 && strings.HasPrefix(location, "https://") {
		return urlSecurityFinding{
			Check:    "https_redirect",
			Severity: "info",
			Status:   "pass",
			Evidence: fmt.Sprintf("redirects to %s", location),
		}
	}

	return urlSecurityFinding{
		Check:       "https_redirect",
		Severity:    "warning",
		Status:      "fail",
		Evidence:    "no HTTPS redirect detected",
		Remediation: "Configure HTTP to HTTPS redirect",
	}
}

func checkURLServerHeader(ctx context.Context, targetURL string) urlSecurityFinding {
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return urlSecurityFinding{
			Check:    "server_header",
			Severity: "info",
			Status:   "fail",
			Evidence: fmt.Sprintf("request error: %v", err),
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		return urlSecurityFinding{
			Check:    "server_header",
			Severity: "info",
			Status:   "fail",
			Evidence: fmt.Sprintf("connection error: %v", err),
		}
	}
	defer func() { _ = resp.Body.Close() }()

	server := resp.Header.Get("Server")
	if server == "" {
		return urlSecurityFinding{
			Check:    "server_header",
			Severity: "info",
			Status:   "pass",
			Evidence: "Server header not exposed",
		}
	}
	return urlSecurityFinding{
		Check:       "server_header",
		Severity:    "info",
		Status:      "fail",
		Evidence:    fmt.Sprintf("Server header exposed: %s", server),
		Remediation: "Remove or obscure the Server header to reduce information disclosure",
	}
}

// extractURLHost strips scheme and path from a URL, returning just the hostname.
func extractURLHost(rawURL string) string {
	rawURL = strings.TrimPrefix(rawURL, "https://")
	rawURL = strings.TrimPrefix(rawURL, "http://")
	// Remove path
	if idx := strings.Index(rawURL, "/"); idx >= 0 {
		rawURL = rawURL[:idx]
	}
	// Remove port
	if idx := strings.Index(rawURL, ":"); idx >= 0 {
		rawURL = rawURL[:idx]
	}
	return rawURL
}
