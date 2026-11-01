package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
)

// agentOperation is the discoverable contract for the non-interactive local
// interface. Mutations are previews unless the caller explicitly uses --apply.
type agentOperation struct {
	Name      string `json:"name"`
	Input     bool   `json:"input"`
	Mutation  bool   `json:"mutation"`
	Sensitive bool   `json:"sensitive_output,omitempty"`
	Summary   string `json:"summary"`
}

var agentOperations = []agentOperation{
	{"capabilities", false, false, false, "List supported operations and safety requirements"},
	{"version", false, false, false, "Read the installed Bot version and build"},
	{"status.get", false, false, false, "Read system and Xray service status"},
	{"config.get", false, false, false, "Read non-secret Bot settings and remote permissions"},
	{"config.server-host", true, true, false, "Set or clear the public address used in REALITY links"},
	{"nodes.list", false, false, false, "List inbound summaries without credentials"},
	{"nodes.get", true, false, false, "Read one inbound summary without credentials"},
	{"nodes.create", true, true, false, "Create one supported VLESS inbound"},
	{"nodes.update", true, true, false, "Change an allowed inbound setting"},
	{"nodes.delete", true, true, false, "Delete an inbound after checking suspended users"},
	{"nodes.public-port", true, true, false, "Set or clear an XHTTP/TLS public port"},
	{"users.list", false, false, false, "List users, assignments, limits and totals"},
	{"users.get", true, false, false, "Read one user without credentials"},
	{"users.export", true, false, true, "Export credential-bearing VLESS links"},
	{"users.add", true, true, false, "Create a user and optionally assign an inbound"},
	{"users.rename", true, true, false, "Rename a user and retain its limits and history"},
	{"users.rotate", true, true, false, "Rotate a user's UUID"},
	{"users.delete", true, true, false, "Delete a user and its assignments"},
	{"users.assign", true, true, false, "Assign a user to an inbound"},
	{"users.unassign", true, true, false, "Remove one user/inbound assignment"},
	{"users.disable", true, true, false, "Suspend a user"},
	{"users.enable", true, true, false, "Restore a suspended user"},
	{"limits.list", false, false, false, "Read user quota and expiry settings"},
	{"limits.quota", true, true, false, "Set or clear a user's byte quota"},
	{"limits.expiry", true, true, false, "Set or clear a user's expiry timestamp"},
	{"limits.clear", true, true, false, "Clear a user's quota and expiry"},
	{"traffic.read", false, false, false, "Read the persistent accounting ledger"},
	{"traffic.snapshot", false, true, false, "Sample and persist Xray traffic"},
	{"traffic.reset-user", true, true, false, "Reset all counters for one user"},
	{"traffic.reset-all", false, true, false, "Reset all user counters"},
	{"online.read", false, false, false, "Read saved online observations"},
	{"online.refresh", false, true, false, "Refresh and persist online observations"},
	{"online.diagnose", false, false, false, "Inspect online policies, collector and freshness"},
	{"online.repair", false, true, false, "Enable the online statistics policies"},
	{"collector.install", false, true, false, "Install or refresh the independent statistics service"},
	{"bindings.migrate", false, true, false, "Migrate client identities to per-inbound accounting"},
	{"bindings.revert", false, true, false, "Revert per-inbound client identities"},
	{"stats.inspect", false, false, false, "Inspect statistics policy and API settings"},
	{"stats.repair", false, true, false, "Repair loopback statistics API and policy switches"},
	{"routing.read", false, false, false, "Read routing, outbound and balancer summaries"},
	{"routing.rules", false, false, false, "Read routing rules and edit references; extensions hidden unless requested"},
	{"routing.rule.patch", true, true, false, "Change one field of a routing rule"},
	{"routing.rule.target", true, true, false, "Change a rule's outbound or balancer"},
	{"routing.rule.move", true, true, false, "Move a routing rule up or down"},
	{"routing.rule.delete", true, true, false, "Delete a routing rule by its edit reference"},
	{"routing.add", true, true, false, "Add a named Xray routing rule"},
	{"routing.update", true, true, false, "Replace a named Xray routing rule"},
	{"routing.delete", true, true, false, "Delete a named Xray routing rule"},
	{"balancers.list", false, false, false, "Read balancers and edit references; extensions hidden unless requested"},
	{"balancers.put", true, true, false, "Create or replace one native balancer"},
	{"balancers.patch", true, true, false, "Change a balancer selector, strategy or fallback"},
	{"balancers.delete", true, true, false, "Delete an unreferenced balancer"},
	{"observatories.list", false, false, false, "Inspect health-check monitors and edit references"},
	{"observatories.put", true, true, false, "Create or replace a native health-check monitor"},
	{"observatories.selectors", true, true, false, "Change a monitor's outbound selectors"},
	{"observatories.patch", true, true, false, "Change a monitor's probe interval or URL"},
	{"observatories.delete", true, true, false, "Delete an unused health-check monitor"},
	{"outbounds.list", false, false, false, "List outbound tags and protocols without credentials"},
	{"outbounds.inspect", false, false, false, "Read outbound summaries, server endpoints and edit references"},
	{"outbounds.create", true, true, false, "Create a direct or blackhole outbound"},
	{"outbounds.endpoint", true, true, false, "Change one proxy server address or port"},
	{"outbounds.default", true, true, false, "Choose the default outbound"},
	{"outbounds.remove", true, true, false, "Delete an outbound by its edit reference"},
	{"outbounds.get", true, false, true, "Export one native outbound including possible credentials"},
	{"outbounds.put", true, true, false, "Add or replace a native outbound"},
	{"outbounds.delete", true, true, false, "Delete an unreferenced non-default outbound"},
	{"dns.inspect", false, false, false, "Read a DNS configuration summary"},
	{"dns.get", false, false, true, "Export native DNS settings including possible credentials"},
	{"dns.put", true, true, false, "Replace the native Xray DNS configuration"},
	{"backup.config", false, false, true, "Export the credential-bearing Xray configuration only"},
	{"service.restart", false, true, false, "Snapshot traffic and restart Xray"},
	{"service.start", false, true, false, "Start the Xray service"},
	{"service.stop", false, true, false, "Snapshot traffic and stop the Xray service"},
	{"updates.check", false, false, false, "Read available manager, Bot and core versions"},
	{"updates.script", false, true, false, "Update the installed manager script"},
	{"updates.bot", false, true, false, "Update the Bot with watchdog rollback"},
	{"updates.core", true, true, false, "Update Xray to a specified stable release"},
	{"updates.core-rollback", false, true, false, "Restore the previous Xray binary"},
}

type agentError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type agentResponse struct {
	SchemaVersion int         `json:"schema_version"`
	Operation     string      `json:"operation"`
	OK            bool        `json:"ok"`
	Data          any         `json:"data,omitempty"`
	Error         *agentError `json:"error,omitempty"`
}

func agentWriteResponse(out io.Writer, operation string, data any, code string, err error) int {
	response := agentResponse{SchemaVersion: 1, Operation: operation, OK: err == nil, Data: data}
	exitCode := 0
	if err != nil {
		response.Data = nil
		response.Error = &agentError{Code: code, Message: err.Error()}
		exitCode = 1
		if code == "invalid_request" || code == "unknown_operation" {
			exitCode = 2
		}
		if code == "permission_denied" {
			exitCode = 3
		}
	}
	encoder := json.NewEncoder(out)
	encoder.SetEscapeHTML(false)
	if encodeErr := encoder.Encode(response); encodeErr != nil {
		return 1
	}
	return exitCode
}

func findAgentOperation(name string) (agentOperation, bool) {
	for _, operation := range agentOperations {
		if operation.Name == name {
			return operation, true
		}
	}
	return agentOperation{}, false
}

func runAgentCLI(args []string, in io.Reader, out io.Writer) int {
	if len(args) == 0 {
		args = []string{"capabilities"}
	}
	name := args[0]
	operation, ok := findAgentOperation(name)
	if !ok {
		return agentWriteResponse(out, name, nil, "unknown_operation", errors.New("unknown operation; use agent capabilities"))
	}
	flags := flag.NewFlagSet("agent "+name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "/usr/local/etc/xray/bot.json", "Bot configuration path")
	apply := flags.Bool("apply", false, "apply a mutation")
	includeSensitive := flags.Bool("include-sensitive", false, "allow credential-bearing output")
	if err := flags.Parse(args[1:]); err != nil {
		return agentWriteResponse(out, name, nil, "invalid_request", err)
	}
	if flags.NArg() != 0 {
		return agentWriteResponse(out, name, nil, "invalid_request", errors.New("unexpected positional arguments"))
	}
	if *apply && !operation.Mutation {
		return agentWriteResponse(out, name, nil, "invalid_request", errors.New("--apply is only valid for mutations"))
	}
	if operation.Sensitive && !*includeSensitive {
		return agentWriteResponse(out, name, nil, "permission_denied", errors.New("operation requires --include-sensitive"))
	}
	var raw []byte
	if operation.Input {
		var err error
		raw, err = io.ReadAll(io.LimitReader(in, 1<<20+1))
		if err != nil {
			return agentWriteResponse(out, name, nil, "invalid_request", err)
		}
		if len(raw) > 1<<20 {
			return agentWriteResponse(out, name, nil, "invalid_request", errors.New("JSON input exceeds 1 MiB"))
		}
		if len(strings.TrimSpace(string(raw))) == 0 {
			return agentWriteResponse(out, name, nil, "invalid_request", errors.New("JSON input is required on stdin"))
		}
	}
	if name == "capabilities" {
		return agentWriteResponse(out, name, map[string]any{"operations": agentOperations, "input": "JSON on stdin", "mutations": "preview by default; pass --apply to execute", "credentials": "export requires --include-sensitive"}, "", nil)
	}
	if name == "version" {
		return agentWriteResponse(out, name, map[string]string{"version": Version, "commit": Commit, "build_time": BuildTime}, "", nil)
	}
	if os.Geteuid() != 0 {
		return agentWriteResponse(out, name, nil, "permission_denied", errors.New("this operation requires root"))
	}
	cfg, err := LoadConfig(*configPath)
	if err != nil {
		return agentWriteResponse(out, name, nil, "operation_failed", err)
	}
	SetXrayBinPath(cfg.XrayBinPath)
	data, err := executeAgentOperation(cfg, operation, raw, *apply, *includeSensitive)
	if err != nil {
		return agentWriteResponse(out, name, nil, "operation_failed", err)
	}
	return agentWriteResponse(out, name, data, "", nil)
}

func decodeAgentInput(raw []byte, destination any) error {
	if trimmed := strings.TrimSpace(string(raw)); !strings.HasPrefix(trimmed, "{") {
		return errors.New("input must be a JSON object")
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("invalid JSON input: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("input must contain exactly one JSON object")
	}
	return nil
}

func agentPreview(operation string, details any) map[string]any {
	return map[string]any{"operation": operation, "applied": false, "preview": details, "note": "Preview is not a guarantee of success: state is rechecked and Xray validates configuration when applied."}
}

type agentNameInput struct {
	User    string `json:"user"`
	Node    string `json:"node,omitempty"`
	NewName string `json:"new_name,omitempty"`
}
type agentTagInput struct {
	Tag string `json:"tag"`
}
type agentPortInput struct {
	Tag        string `json:"tag"`
	PublicPort *int   `json:"public_port"`
}
type agentLimitInput struct {
	User       string `json:"user"`
	QuotaBytes *int64 `json:"quota_bytes,omitempty"`
	ExpiryUnix *int64 `json:"expiry_unix,omitempty"`
}
type agentUpdateInput struct {
	Version string `json:"version"`
}

type agentNodeSummary struct {
	Tag        string   `json:"tag"`
	Port       int      `json:"port"`
	PublicPort int      `json:"public_port"`
	Protocol   string   `json:"protocol"`
	Network    string   `json:"network"`
	Security   string   `json:"security"`
	SNI        string   `json:"sni,omitempty"`
	XHTTPPath  string   `json:"xhttp_path,omitempty"`
	XHTTPHost  string   `json:"xhttp_host,omitempty"`
	XHTTPMode  string   `json:"xhttp_mode,omitempty"`
	Users      []string `json:"users"`
}

func agentNodes(configPath string) ([]agentNodeSummary, error) {
	nodes, err := LoadXrayInbounds(configPath)
	if err != nil {
		return nil, err
	}
	result := make([]agentNodeSummary, 0, len(nodes))
	for _, node := range nodes {
		users := make([]string, 0, len(node.Clients))
		for _, client := range node.Clients {
			users = append(users, bindingOwner(client.Email))
		}
		result = append(result, agentNodeSummary{Tag: node.Tag, Port: node.Port, PublicPort: node.SharePort(), Protocol: node.Protocol, Network: node.Network, Security: node.Security, SNI: node.SNI, XHTTPPath: node.XHTTPPath, XHTTPHost: node.XHTTPHost, XHTTPMode: node.XHTTPMode, Users: sortedUniqueStrings(users)})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Tag < result[j].Tag })
	return result, nil
}

type agentUserSummary struct {
	User       string         `json:"user"`
	Nodes      []string       `json:"nodes"`
	Suspended  bool           `json:"suspended"`
	Traffic    trafficCounter `json:"traffic"`
	Limit      userLimit      `json:"limit"`
	LastOnline int64          `json:"last_online,omitempty"`
}

func agentUsers(configPath string) ([]agentUserSummary, error) {
	lock, err := acquireConfigLock(configPath, 10*time.Second)
	if err != nil {
		return nil, err
	}
	defer releaseConfigLock(lock)
	names, err := allDirectoryNames(configPath)
	if err != nil {
		return nil, err
	}
	suspended, _, err := readSuspendedStore(configPath)
	if err != nil {
		return nil, err
	}
	for name := range suspended.Users {
		names = append(names, name)
	}
	names = sortedUniqueStrings(names)
	nodes, err := agentNodes(configPath)
	if err != nil {
		return nil, err
	}
	state, err := loadTrafficStore(configPath)
	if err != nil {
		return nil, err
	}
	limits, err := snapshotUserLimits(configPath)
	if err != nil {
		return nil, err
	}
	result := make([]agentUserSummary, 0, len(names))
	for _, name := range names {
		item := agentUserSummary{User: name, Nodes: []string{}, Traffic: state.Users[name], Limit: limits[name], LastOnline: state.LastOnline[name]}
		_, item.Suspended = suspended.Users[name]
		for _, node := range nodes {
			for _, user := range node.Users {
				if user == name {
					item.Nodes = append(item.Nodes, node.Tag)
				}
			}
		}
		result = append(result, item)
	}
	return result, nil
}

func agentExportUser(cfg *BotConfig, user string) ([]map[string]string, error) {
	if !managedUserName.MatchString(user) {
		return nil, errors.New("invalid user identifier")
	}
	nodes, err := LoadXrayInbounds(cfg.XrayConfigPath)
	if err != nil {
		return nil, err
	}
	entries := boundUserExportEntries(nodes, user)
	if len(entries) == 0 {
		return nil, errors.New("user has no assigned VLESS inbound")
	}
	host, hostErr := resolveServerHost(cfg)
	if hostErr != nil {
		host = ""
	}
	links := make([]map[string]string, 0, len(entries))
	for _, entry := range entries {
		uri, err := validatedUserExportURI(entry, host)
		if err != nil {
			return nil, err
		}
		links = append(links, map[string]string{"tag": entry.Node.Tag, "uri": uri})
	}
	return links, nil
}

func executeAgentOperation(cfg *BotConfig, operation agentOperation, raw []byte, apply, includeSensitive bool) (any, error) {
	name := operation.Name
	switch name {
	case "status.get":
		stats, err := GetSystemStats()
		if err != nil {
			return nil, err
		}
		return map[string]any{"hostname": stats.Hostname, "platform": stats.Platform, "system_uptime": stats.UptimeStr,
			"cpu_cores": stats.CPUCores, "cpu_usage_percent": stats.CPUUsagePct, "load_average": []string{stats.Load1, stats.Load5, stats.Load15},
			"memory_total_bytes": stats.MemTotalBytes, "memory_used_bytes": stats.MemUsedBytes, "memory_usage_percent": stats.MemUsagePct,
			"disk_total_bytes": stats.DiskTotalBytes, "disk_used_bytes": stats.DiskUsedBytes, "disk_usage_percent": stats.DiskUsagePct, "xray_status": stats.XrayStatus}, nil
	case "config.get":
		return map[string]any{"server_host": cfg.ServerHost, "language": cfg.Language, "allow_mutations": cfg.AllowMutations, "allow_updates": cfg.AllowUpdates, "allow_core_updates": cfg.AllowCoreUpdates, "allow_sensitive_backup": cfg.AllowSensitiveBackup, "xray_config_path": cfg.XrayConfigPath, "xray_bin_path": cfg.XrayBinPath, "stats_api_server": cfg.StatsAPIServer}, nil
	case "config.server-host":
		var input struct {
			Host *string `json:"host"`
		}
		if err := decodeAgentInput(raw, &input); err != nil {
			return nil, err
		}
		if input.Host == nil {
			return nil, errors.New("host is required; use an empty string for automatic discovery")
		}
		host, err := normalizeServerHost(*input.Host)
		if err != nil {
			return nil, err
		}
		if !apply {
			return agentPreview(name, map[string]any{"host": host}), nil
		}
		if _, err = persistServerHost(cfg.ConfigFile, host); err != nil {
			return nil, err
		}
		return map[string]any{"applied": true, "host": host}, nil
	case "nodes.list":
		return agentNodes(cfg.XrayConfigPath)
	case "nodes.get", "nodes.delete":
		var input agentTagInput
		if err := decodeAgentInput(raw, &input); err != nil {
			return nil, err
		}
		if err := validateNodeTag(input.Tag); err != nil {
			return nil, err
		}
		if name == "nodes.delete" {
			report, err := inspectNodeDeletion(cfg.XrayConfigPath, input.Tag)
			if err != nil {
				return nil, err
			}
			if !apply {
				return agentPreview(name, report), nil
			}
			return agentMessage(DeleteNode(cfg.XrayConfigPath, cfg.XrayBinPath, cfg.StatsAPIServer, input.Tag))
		}
		nodes, err := agentNodes(cfg.XrayConfigPath)
		if err != nil {
			return nil, err
		}
		for _, node := range nodes {
			if node.Tag == input.Tag {
				return node, nil
			}
		}
		return nil, errors.New("inbound not found")
	case "nodes.create":
		return agentCreateNode(cfg, raw, apply)
	case "nodes.update":
		return agentUpdateNode(cfg, raw, apply)
	case "nodes.public-port":
		var input agentPortInput
		if err := decodeAgentInput(raw, &input); err != nil {
			return nil, err
		}
		if err := validateNodeTag(input.Tag); err != nil {
			return nil, err
		}
		if input.PublicPort == nil || *input.PublicPort < 0 || *input.PublicPort > 65535 {
			return nil, errors.New("public_port must be between 0 and 65535")
		}
		if !apply {
			return agentPreview(name, input), nil
		}
		return agentMessage(SetInboundPublicPort(cfg.XrayConfigPath, input.Tag, *input.PublicPort))
	case "users.list":
		return agentUsers(cfg.XrayConfigPath)
	case "users.get", "users.export":
		var input agentNameInput
		if err := decodeAgentInput(raw, &input); err != nil {
			return nil, err
		}
		if !managedUserName.MatchString(input.User) {
			return nil, errors.New("invalid user identifier")
		}
		if input.Node != "" || input.NewName != "" {
			return nil, errors.New("this operation accepts only user")
		}
		if name == "users.export" {
			if !includeSensitive {
				return nil, errors.New("sensitive output is not enabled")
			}
			return agentExportUser(cfg, input.User)
		}
		users, err := agentUsers(cfg.XrayConfigPath)
		if err != nil {
			return nil, err
		}
		for _, user := range users {
			if user.User == input.User {
				return user, nil
			}
		}
		return nil, errors.New("user not found")
	case "users.add", "users.rename", "users.rotate", "users.delete", "users.assign", "users.unassign", "users.disable", "users.enable":
		var input agentNameInput
		if err := decodeAgentInput(raw, &input); err != nil {
			return nil, err
		}
		if !managedUserName.MatchString(input.User) {
			return nil, errors.New("invalid user identifier")
		}
		kind := strings.TrimPrefix(name, "users.")
		if kind == "rename" && !managedUserName.MatchString(input.NewName) {
			return nil, errors.New("invalid new user identifier")
		}
		if (kind == "assign" || kind == "unassign") && (input.Node == "" || input.Node == "all") {
			return nil, errors.New("a specific node is required")
		}
		if input.NewName != "" && kind != "rename" {
			return nil, errors.New("new_name is only valid for users.rename")
		}
		if input.Node != "" && kind != "add" && kind != "assign" && kind != "unassign" {
			return nil, errors.New("node is not valid for this operation")
		}
		if input.Node != "" && input.Node != "all" {
			if err := validateNodeTag(input.Node); err != nil {
				return nil, err
			}
		}
		if !apply {
			return agentPreview(name, input), nil
		}
		_, err := changeManagedUser(cfg.XrayConfigPath, cfg.XrayBinPath, cfg.StatsAPIServer, UserChange{Kind: kind, User: input.User, Node: input.Node, NewName: input.NewName})
		if err != nil {
			return nil, err
		}
		// Management messages can contain generated UUIDs. Obtain credentials
		// only through the explicitly opted-in users.export operation.
		return map[string]any{"applied": true, "user": input.User, "operation": kind}, nil
	case "limits.list":
		return snapshotUserLimits(cfg.XrayConfigPath)
	case "limits.quota", "limits.expiry", "limits.clear":
		return agentSetLimit(cfg, name, raw, apply)
	case "traffic.read":
		return loadTrafficStore(cfg.XrayConfigPath)
	case "traffic.snapshot":
		if !apply {
			return agentPreview(name, "sample Xray and update the persistent ledger"), nil
		}
		return snapshotTraffic(cfg.XrayConfigPath, cfg.XrayBinPath, cfg.StatsAPIServer)
	case "traffic.reset-user":
		var input agentNameInput
		if err := decodeAgentInput(raw, &input); err != nil {
			return nil, err
		}
		if !managedUserName.MatchString(input.User) {
			return nil, errors.New("invalid user identifier")
		}
		if input.Node != "" || input.NewName != "" {
			return nil, errors.New("traffic.reset-user accepts only user")
		}
		if !apply {
			return agentPreview(name, input), nil
		}
		if err := resetUserTraffic(cfg.XrayConfigPath, cfg.XrayBinPath, cfg.StatsAPIServer, input.User); err != nil {
			return nil, err
		}
		return map[string]any{"applied": true}, nil
	case "traffic.reset-all":
		if !apply {
			return agentPreview(name, "reset all users' Xray and persistent counters"), nil
		}
		if err := agentResetAllTraffic(cfg); err != nil {
			return nil, err
		}
		return map[string]any{"applied": true}, nil
	case "online.read":
		state, err := loadTrafficStore(cfg.XrayConfigPath)
		if err != nil {
			return nil, err
		}
		_, fresh := recentOnline(state, "", time.Now())
		return map[string]any{"poll_at": state.OnlinePollAt, "fresh": fresh, "live_ips": state.LiveIPs, "recent_ips": state.OnlineIPs, "last_online": state.LastOnline, "last_active": state.LastActive}, nil
	case "online.refresh":
		if !apply {
			return agentPreview(name, "query Xray and update online observations"), nil
		}
		return refreshOnlineTraffic(cfg.XrayConfigPath, cfg.XrayBinPath, cfg.StatsAPIServer)
	case "online.diagnose":
		levels, err := inspectOnlinePolicy(cfg.XrayConfigPath)
		if err != nil {
			return nil, err
		}
		state, err := loadTrafficStore(cfg.XrayConfigPath)
		if err != nil {
			return nil, err
		}
		_, fresh := recentOnline(state, "", time.Now())
		return map[string]any{"levels": levels, "collector_active": trafficCollectorActive(), "poll_at": state.OnlinePollAt, "fresh": fresh}, nil
	case "online.repair":
		if !apply {
			return agentPreview(name, "enable online and traffic policy switches; restart Xray if needed"), nil
		}
		if err := ensureOnlineStatsPolicy(cfg); err != nil {
			return nil, err
		}
		return map[string]any{"applied": true}, nil
	case "collector.install":
		if !apply {
			return agentPreview(name, "install and start freedom-stats.service; policy changes can restart Xray"), nil
		}
		if cfg.ConfigFile == "" {
			return nil, errors.New("Bot configuration path is unavailable")
		}
		if err := requireInstalledAgent(); err != nil {
			return nil, err
		}
		if err := ensureTrafficCollectorUnit(cfg.ConfigFile, cfg); err != nil {
			return nil, err
		}
		return map[string]any{"applied": true, "collector_active": trafficCollectorActive()}, nil
	case "bindings.migrate", "bindings.revert":
		mode := bindingMigrate
		if name == "bindings.revert" {
			mode = bindingRevert
		}
		if !apply {
			report, err := preflightBindingMigration(cfg.XrayConfigPath, mode)
			if err != nil {
				return nil, err
			}
			return agentPreview(name, report), nil
		}
		if mode == bindingMigrate {
			return migrateBindingEmailsForced(cfg.XrayConfigPath, cfg.XrayBinPath, cfg.StatsAPIServer)
		}
		return revertBindingEmails(cfg.XrayConfigPath, cfg.XrayBinPath, cfg.StatsAPIServer)
	case "stats.inspect":
		return inspectStatsSwitches(cfg.XrayConfigPath, cfg.StatsAPIServer)
	case "stats.repair":
		if !apply {
			return agentPreview(name, "enable statistics policy and restart Xray if required"), nil
		}
		if err := ensureStatsInfrastructure(cfg); err != nil {
			return nil, err
		}
		return inspectStatsSwitches(cfg.XrayConfigPath, cfg.StatsAPIServer)
	case "routing.read":
		return LoadRouting(cfg.XrayConfigPath)
	case "routing.rules":
		records, err := loadRoutingRuleRecords(cfg.XrayConfigPath)
		if err != nil || includeSensitive {
			return records, err
		}
		return redactRoutingRuleRecords(records), nil
	case "routing.rule.patch", "routing.rule.target", "routing.rule.move", "routing.rule.delete":
		return agentRoutingRuleEdit(cfg, name, raw, apply)
	case "routing.add", "routing.update", "routing.delete":
		return agentRoutingChange(cfg, name, raw, apply)
	case "balancers.list":
		root, err := decodeBindingMigrationConfig(cfg.XrayConfigPath)
		if err != nil {
			return nil, err
		}
		records, err := balancerRecords(root)
		if err != nil || includeSensitive {
			return records, err
		}
		return redactBalancerRecords(records), nil
	case "balancers.put", "balancers.patch", "balancers.delete":
		return agentBalancerChange(cfg, name, raw, apply)
	case "observatories.list":
		root, err := decodeBindingMigrationConfig(cfg.XrayConfigPath)
		if err != nil {
			return nil, err
		}
		return observatoryRecords(root, includeSensitive)
	case "observatories.put", "observatories.selectors", "observatories.patch", "observatories.delete":
		return agentObservatoryChange(cfg, name, raw, apply)
	case "outbounds.inspect":
		root, err := decodeBindingMigrationConfig(cfg.XrayConfigPath)
		if err != nil {
			return nil, err
		}
		return outboundRecords(root)
	case "outbounds.create", "outbounds.endpoint", "outbounds.default", "outbounds.remove":
		return agentOutboundChange(cfg, name, raw, apply)
	case "outbounds.list", "dns.inspect":
		outbounds, dns, err := networkSummary(cfg.XrayConfigPath)
		if err != nil {
			return nil, err
		}
		if name == "dns.inspect" {
			return dns, nil
		}
		return outbounds, nil
	case "outbounds.get", "dns.get":
		if !includeSensitive {
			return nil, errors.New("sensitive output is not enabled")
		}
		root, err := decodeBindingMigrationConfig(cfg.XrayConfigPath)
		if err != nil {
			return nil, err
		}
		if name == "dns.get" {
			return root["dns"], nil
		}
		var input agentTagInput
		if err = decodeAgentInput(raw, &input); err != nil {
			return nil, err
		}
		if err = validateNodeTag(input.Tag); err != nil {
			return nil, err
		}
		values, _ := root["outbounds"].([]any)
		for _, value := range values {
			item, _ := getMap(value)
			if item["tag"] == input.Tag {
				return item, nil
			}
		}
		return nil, errors.New("outbound not found")
	case "outbounds.put", "outbounds.delete", "dns.put":
		return agentNetworkChange(cfg, name, raw, apply)
	case "backup.config":
		if !includeSensitive {
			return nil, errors.New("sensitive output is not enabled")
		}
		root, err := decodeBindingMigrationConfig(cfg.XrayConfigPath)
		if err != nil {
			return nil, err
		}
		return root, nil
	case "service.start", "service.stop", "service.restart":
		action := strings.TrimPrefix(name, "service.")
		if !apply {
			return agentPreview(name, "control the Xray service; stop/restart snapshot traffic best-effort"), nil
		}
		if err := manageXrayService(cfg, action); err != nil {
			return nil, err
		}
		return map[string]any{"applied": true}, nil
	case "updates.check", "updates.script", "updates.bot", "updates.core", "updates.core-rollback":
		return agentUpdateComponent(cfg, name, raw, apply)
	}
	return nil, errors.New("operation is not implemented")
}

func agentMessage(message string, err error) (any, error) {
	if err != nil {
		return nil, err
	}
	return map[string]any{"applied": true, "message": message}, nil
}

func agentResetAllTraffic(cfg *BotConfig) error {
	lock, err := acquireConfigLock(cfg.XrayConfigPath, 10*time.Second)
	if err != nil {
		return err
	}
	defer releaseConfigLock(lock)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err = QueryTrafficStats(ctx, cfg.XrayBinPath, cfg.StatsAPIServer, true); err != nil {
		return err
	}
	if err = resetTrafficAllUsersLocked(cfg.XrayConfigPath); err != nil {
		return fmt.Errorf("Xray counters were reset but ledger reset failed: %w", err)
	}
	return nil
}
