package server

import (
	"context"
	"embed"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/templui/templui/components/badge"
	"github.com/templui/templui/components/button"
	"github.com/templui/templui/components/card"
	"github.com/templui/templui/components/table"

	"gatelet/internal/protocol"
)

//go:embed admin_assets/*
var adminAssets embed.FS

type adminDashboardData struct {
	Domain      string
	ActionToken string
	Notice      string
	Uptime      string
	Totals      statusTotals
	Tunnels     []TunnelStats
}

func (s *Server) serveAdmin(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && (r.URL.Path == "/admin" || r.URL.Path == "/admin/"):
		s.renderAdminPage(w, r, "")
	case r.Method == http.MethodGet && r.URL.Path == "/admin/partials/dashboard":
		s.renderAdminDashboard(w, r, "")
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/admin/tunnels/") && strings.HasSuffix(r.URL.Path, "/disconnect"):
		s.handleAdminDisconnect(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/admin/assets/"):
		s.serveAdminAsset(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) renderAdminPage(w http.ResponseWriter, r *http.Request, notice string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := adminPage(s.adminDashboardData(notice)).Render(templ.InitializeContext(r.Context()), w); err != nil {
		s.logger.Error("render admin page failed", "error", err)
	}
}

func (s *Server) renderAdminDashboard(w http.ResponseWriter, r *http.Request, notice string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := adminDashboard(s.adminDashboardData(notice)).Render(templ.InitializeContext(r.Context()), w); err != nil {
		s.logger.Error("render admin dashboard failed", "error", err)
	}
}

func (s *Server) handleAdminDisconnect(w http.ResponseWriter, r *http.Request) {
	if !s.validAdminActionToken(r) {
		http.Error(w, "invalid admin action token", http.StatusForbidden)
		return
	}

	name := strings.TrimPrefix(r.URL.Path, "/admin/tunnels/")
	name = strings.TrimSuffix(name, "/disconnect")
	unescaped, err := url.PathUnescape(name)
	if err != nil || protocol.ValidateName(unescaped) != nil {
		http.NotFound(w, r)
		return
	}

	notice := "Tunnel " + unescaped + " was not active"
	if s.DisconnectTunnel(unescaped) {
		notice = "Disconnected tunnel " + unescaped
	}
	s.renderAdminDashboard(w, r, notice)
}

func (s *Server) serveAdminAsset(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/admin/assets/")
	if strings.Contains(name, "/") || name == "" {
		http.NotFound(w, r)
		return
	}

	data, err := adminAssets.ReadFile("admin_assets/" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if strings.HasSuffix(name, ".js") {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	}
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(data)
}

func (s *Server) adminDashboardData(notice string) adminDashboardData {
	tunnels := s.allTunnelStats()
	data := adminDashboardData{
		Domain:      s.domain,
		ActionToken: s.adminActionToken,
		Notice:      notice,
		Uptime:      formatDuration(time.Since(s.startedAt)),
		Tunnels:     tunnels,
	}
	for _, tunnel := range tunnels {
		data.Totals.Requests += tunnel.Requests
		data.Totals.BytesIn += tunnel.BytesIn
		data.Totals.BytesOut += tunnel.BytesOut
	}
	return data
}

func adminPage(data adminDashboardData) templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		if _, err := fmt.Fprintf(w, `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="gatelet-admin-action-token" content="%s"><title>Gatelet Admin</title><style>`, templ.EscapeString(data.ActionToken)); err != nil {
			return err
		}
		if _, err := io.WriteString(w, adminCSS); err != nil {
			return err
		}
		if _, err := io.WriteString(w, `</style><script defer src="/admin/assets/alpine.min.js"></script><script src="/admin/assets/htmx.min.js"></script></head><body>`); err != nil {
			return err
		}
		if err := adminDashboard(data).Render(ctx, w); err != nil {
			return err
		}
		_, err := io.WriteString(w, `</body></html>`)
		return err
	})
}

func adminDashboard(data adminDashboardData) templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		if _, err := io.WriteString(w, `<main id="admin-dashboard" class="admin-shell" x-data hx-get="/admin/partials/dashboard" hx-trigger="every 5s" hx-swap="outerHTML">`); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, `<header class="admin-header"><div><p class="admin-kicker">Gatelet relay</p><h1>%s</h1></div><div class="admin-links"><a href="/__gatelet/status">status JSON</a><a href="/metrics">metrics</a></div></header>`, templ.EscapeString(data.Domain)); err != nil {
			return err
		}
		if data.Notice != "" {
			if _, err := fmt.Fprintf(w, `<div class="admin-notice">%s</div>`, templ.EscapeString(data.Notice)); err != nil {
				return err
			}
		}
		if _, err := io.WriteString(w, `<section class="admin-summary">`); err != nil {
			return err
		}
		for _, item := range []struct {
			label string
			value string
			hint  string
		}{
			{"Uptime", data.Uptime, "daemon runtime"},
			{"Active tunnels", fmt.Sprint(len(data.Tunnels)), "connected clients"},
			{"Requests", fmt.Sprint(data.Totals.Requests), "forwarded total"},
			{"Traffic", formatBytes(data.Totals.BytesIn) + " in / " + formatBytes(data.Totals.BytesOut) + " out", "HTTP payload bytes"},
		} {
			if err := renderSummaryCard(ctx, w, item.label, item.value, item.hint); err != nil {
				return err
			}
		}
		if _, err := io.WriteString(w, `</section>`); err != nil {
			return err
		}
		if err := renderTunnelTable(ctx, w, data); err != nil {
			return err
		}
		_, err := io.WriteString(w, `</main>`)
		return err
	})
}

func renderSummaryCard(ctx context.Context, w io.Writer, label, value, hint string) error {
	return withChildren(card.Card(card.Props{Class: "admin-card"}), join(
		withChildren(card.Header(), join(
			raw(`<p class="admin-card-label">`),
			text(label),
			raw(`</p>`),
			withChildren(card.Title(card.TitleProps{Class: "admin-card-value"}), text(value)),
		)),
		withChildren(card.Content(), join(
			raw(`<p class="admin-card-hint">`),
			text(hint),
			raw(`</p>`),
		)),
	)).Render(ctx, w)
}

func renderTunnelTable(ctx context.Context, w io.Writer, data adminDashboardData) error {
	return withChildren(card.Card(card.Props{Class: "admin-card admin-table-card"}), join(
		withChildren(card.Header(), join(
			withChildren(card.Title(), text("Active tunnels")),
			raw(`<p class="admin-card-hint">Live tunnel sessions registered on this relay.</p>`),
		)),
		withChildren(card.Content(), withChildren(table.Table(table.Props{Class: "admin-table"}), join(
			withChildren(table.Header(), withChildren(table.Row(), join(
				tableHead("Name"),
				tableHead("Public URL"),
				tableHead("Remote"),
				tableHead("Connected"),
				tableHead("Last seen"),
				tableHead("Requests"),
				tableHead("Bytes"),
				tableHead("Statuses"),
				tableHead(""),
			))),
			withChildren(table.Body(), tunnelRows(data)),
		))),
	)).Render(ctx, w)
}

func tunnelRows(data adminDashboardData) templ.Component {
	if len(data.Tunnels) == 0 {
		return withChildren(table.Row(), withChildren(table.Cell(table.CellProps{Class: "admin-empty", Attributes: templ.Attributes{"colspan": "9"}}), text("No active tunnels")))
	}

	rows := make([]templ.Component, 0, len(data.Tunnels))
	for _, tunnel := range data.Tunnels {
		publicURL := publicTunnelURL(data.Domain, tunnel)
		rows = append(rows, withChildren(table.Row(), join(
			withChildren(table.Cell(), withChildren(badge.Badge(badge.Props{Variant: badge.VariantSecondary, Class: "admin-badge"}), text(tunnel.Name))),
			withChildren(table.Cell(table.CellProps{Class: "admin-mono"}), text(publicURL)),
			withChildren(table.Cell(table.CellProps{Class: "admin-mono"}), text(tunnel.Remote)),
			withChildren(table.Cell(), text(formatClock(tunnel.ConnectedAt))),
			withChildren(table.Cell(), text(formatAge(time.Since(tunnel.LastSeen)))),
			withChildren(table.Cell(), text(fmt.Sprint(tunnel.Requests))),
			withChildren(table.Cell(), text(formatBytes(tunnel.BytesIn)+" in / "+formatBytes(tunnel.BytesOut)+" out")),
			withChildren(table.Cell(), text(statusCounts(tunnel.StatusCounts))),
			withChildren(table.Cell(table.CellProps{Class: "admin-actions"}), disconnectButton(data.ActionToken, tunnel.Name)),
		)))
	}
	return join(rows...)
}

func publicTunnelURL(domain string, tunnel TunnelStats) string {
	if tunnel.TunnelType == protocol.TunnelTypeTCP {
		return fmt.Sprintf("tcp://%s.%s:%d", tunnel.Name, domain, tunnel.RemotePort)
	}
	return "https://" + tunnel.Name + "." + domain
}

func disconnectButton(token, name string) templ.Component {
	confirm := "return confirm('Disconnect tunnel " + jsString(name) + "?')"
	return withChildren(button.Button(button.Props{
		Variant: button.VariantDestructive,
		Size:    button.SizeSm,
		Class:   "admin-disconnect",
		Attributes: templ.Attributes{
			"hx-post":    "/admin/tunnels/" + url.PathEscape(name) + "/disconnect",
			"hx-target":  "#admin-dashboard",
			"hx-swap":    "outerHTML",
			"hx-headers": `{"X-Gatelet-Admin-Token":"` + token + `"}`,
			"x-on:click": confirm,
		},
	}), text("Disconnect"))
}

func tableHead(label string) templ.Component {
	return withChildren(table.Head(), text(label))
}

func withChildren(component templ.Component, children templ.Component) templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		return component.Render(templ.WithChildren(ctx, children), w)
	})
}

func join(children ...templ.Component) templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		for _, child := range children {
			if err := child.Render(ctx, w); err != nil {
				return err
			}
		}
		return nil
	})
}

func text(s string) templ.Component {
	return templ.ComponentFunc(func(_ context.Context, w io.Writer) error {
		_, err := io.WriteString(w, templ.EscapeString(s))
		return err
	})
}

func raw(s string) templ.Component {
	return templ.Raw(s)
}

func statusCounts(counts map[int]uint64) string {
	if len(counts) == 0 {
		return "-"
	}
	statuses := make([]int, 0, len(counts))
	for status := range counts {
		statuses = append(statuses, status)
	}
	sort.Ints(statuses)
	parts := make([]string, 0, len(statuses))
	for _, status := range statuses {
		parts = append(parts, fmt.Sprintf("%d:%d", status, counts[status]))
	}
	return strings.Join(parts, " ")
}

func formatBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1fGB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fKB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
}

func formatAge(d time.Duration) string {
	if d < time.Second {
		return "now"
	}
	return formatDuration(d) + " ago"
}

func formatClock(t time.Time) string {
	return t.Format("2006-01-02 15:04:05")
}

func jsString(s string) string {
	return strings.NewReplacer(`\`, `\\`, `'`, `\'`, "\n", `\n`, "\r", `\r`).Replace(s)
}

const adminCSS = `
:root {
  color-scheme: dark;
  --bg: #0b1020;
  --panel: #111827;
  --panel-2: #162033;
  --line: #26364f;
  --text: #e5edf7;
  --muted: #8da2bd;
  --accent: #58c4a6;
  --danger: #ef5d64;
}
* { box-sizing: border-box; }
body { margin: 0; background: var(--bg); color: var(--text); font: 14px/1.45 ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; }
a { color: var(--accent); text-decoration: none; }
a:hover { text-decoration: underline; }
.admin-shell { min-height: 100vh; padding: 28px; }
.admin-header { display: flex; align-items: flex-end; justify-content: space-between; gap: 20px; margin-bottom: 22px; }
.admin-header h1 { margin: 0; font-size: 28px; letter-spacing: 0; }
.admin-kicker { margin: 0 0 5px; color: var(--muted); text-transform: uppercase; font-size: 12px; letter-spacing: .08em; }
.admin-links { display: flex; gap: 14px; color: var(--muted); }
.admin-summary { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 14px; margin-bottom: 16px; }
.admin-card { background: var(--panel); border: 1px solid var(--line); border-radius: 8px; padding: 16px; box-shadow: 0 18px 50px rgba(0,0,0,.25); }
.admin-card-label, .admin-card-hint { margin: 0; color: var(--muted); }
.admin-card-value { margin: 6px 0 0; font-size: 24px; font-weight: 700; letter-spacing: 0; }
.admin-table-card { overflow-x: auto; }
.admin-table { width: 100%; border-collapse: collapse; }
.admin-table th { color: var(--muted); font-weight: 600; text-align: left; border-bottom: 1px solid var(--line); padding: 10px 8px; white-space: nowrap; }
.admin-table td { border-bottom: 1px solid rgba(38,54,79,.65); padding: 11px 8px; vertical-align: middle; white-space: nowrap; }
.admin-table tr:hover td { background: rgba(88,196,166,.06); }
.admin-mono { font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; font-size: 12px; }
.admin-badge { display: inline-flex; align-items: center; border-radius: 999px; background: rgba(88,196,166,.12); color: var(--accent); border: 1px solid rgba(88,196,166,.35); padding: 3px 8px; }
.admin-actions { text-align: right; }
.admin-disconnect { appearance: none; border: 1px solid rgba(239,93,100,.4); border-radius: 6px; background: rgba(239,93,100,.14); color: #ffd6d9; padding: 6px 10px; cursor: pointer; }
.admin-disconnect:hover { background: rgba(239,93,100,.24); }
.admin-empty { color: var(--muted); text-align: center; padding: 32px 8px !important; }
.admin-notice { border: 1px solid rgba(88,196,166,.35); background: rgba(88,196,166,.10); color: #b7f7e6; border-radius: 8px; padding: 10px 12px; margin-bottom: 14px; }
@media (max-width: 900px) {
  .admin-shell { padding: 18px; }
  .admin-header { align-items: flex-start; flex-direction: column; }
  .admin-summary { grid-template-columns: repeat(2, minmax(0, 1fr)); }
}
@media (max-width: 560px) {
  .admin-summary { grid-template-columns: 1fr; }
}
`
