package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type directoryUser struct {
	ID string `json:"id"`
}
type userDirectory struct {
	Users map[string]directoryUser `json:"users"`
}

func directoryPath(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "freedom-users.json")
}

// Existing inbounds remain the source of truth for legacy credentials; the
// sidecar provides persistence for users not bound to any inbound.
func directoryFromConfig(root map[string]any) (userDirectory, error) {
	directory := userDirectory{Users: map[string]directoryUser{}}
	inbounds, err := usableInbounds(root)
	if err != nil {
		return directory, err
	}
	for _, in := range inbounds {
		clients, _ := clientList(in)
		for _, v := range clients {
			c, ok := getMap(v)
			if !ok {
				return directory, errors.New("Invalid user configuration")
			}
			email, _ := c["email"].(string)
			id, _ := c["id"].(string)
			if email == "" || id == "" {
				continue
			}
			owner := bindingOwner(email)
			if owner == "" {
				continue
			}
			if saved, ok := directory.Users[owner]; ok && saved.ID != id {
				return directory, fmt.Errorf("User %s has conflicting UUIDs across inbounds; fix locally before proceeding", owner)
			}
			directory.Users[owner] = directoryUser{ID: id}
		}
	}
	return directory, nil
}
func loadUserDirectory(configPath string, root map[string]any) (userDirectory, []byte, error) {
	merged, err := directoryFromConfig(root)
	if err != nil {
		return merged, nil, err
	}
	old, err := os.ReadFile(directoryPath(configPath))
	if os.IsNotExist(err) {
		return merged, nil, nil
	}
	if err != nil {
		return merged, nil, err
	}
	var saved userDirectory
	if err = json.Unmarshal(old, &saved); err != nil {
		return merged, nil, fmt.Errorf("User directory is corrupted: %w", err)
	}
	for name, u := range saved.Users {
		if !managedUserName.MatchString(name) || u.ID == "" {
			return merged, nil, errors.New("User directory contains an invalid name or empty UUID")
		}
		if existing, ok := merged.Users[name]; ok && existing.ID != u.ID {
			return merged, nil, fmt.Errorf("User %s has a directory UUID that does not match the Xray configuration", name)
		}
		merged.Users[name] = u
	}
	return merged, old, nil
}

func allDirectoryNames(configPath string) ([]string, error) {
	config, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}
	var root map[string]any
	if err = json.Unmarshal(config, &root); err != nil {
		return nil, err
	}
	dir, _, err := loadUserDirectory(configPath, root)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(dir.Users))
	for name := range dir.Users {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

func userFlowForInbound(in map[string]any) string {
	stream, ok := getMap(in["streamSettings"])
	if !ok {
		return ""
	}
	network, _ := stream["network"].(string)
	security, _ := stream["security"].(string)
	settings, _ := getMap(in["settings"])
	vdec, _ := settings["decryption"].(string)
	allowed := (network == "tcp" || network == "raw") && (security == "tls" || security == "reality") ||
		vdec != "" && vdec != "none"
	if !allowed {
		return ""
	}
	clients, _ := clientList(in)
	for _, c := range clients {
		client, _ := getMap(c)
		if flow, ok := client["flow"].(string); ok && flow == "xtls-rprx-vision" {
			return flow
		}
	}
	// Existing users without Vision should not unexpectedly acquire it when a
	// new account is added. For an empty inbound use transport policy defaults.
	if len(clients) > 0 {
		return ""
	}
	return "xtls-rprx-vision"
}

func applyDirectoryUserChange(root map[string]any, dir *userDirectory, change UserChange, perNodeEmails bool) (string, error) {
	if !managedUserName.MatchString(change.User) {
		return "", errors.New("Usernames must be 1–48 ASCII letters, digits, _, ., @, or -")
	}
	if dir.Users == nil {
		dir.Users = map[string]directoryUser{}
	}
	inbounds, err := usableInbounds(root)
	if err != nil {
		return "", err
	}
	existing, exists := dir.Users[change.User]
	switch change.Kind {
	case "add":
		if exists {
			return "", errors.New("User already exists")
		}
		uuid, err := newManagedUUID()
		if err != nil {
			return "", err
		}
		if change.Node != "" && change.Node != "all" {
			found := false
			for _, in := range inbounds {
				if in["tag"] == change.Node {
					found = true
				}
			}
			if !found {
				return "", errors.New("Target inbound does not exist")
			}
		}
		dir.Users[change.User] = directoryUser{ID: uuid}
		if change.Node == "" {
			return "Created independent user " + change.User + "; no inbound assigned, UUID: " + uuid, nil
		}
		for _, in := range inbounds {
			if change.Node != "all" && in["tag"] != change.Node {
				continue
			}
			tag, _ := in["tag"].(string)
			if err := validateNodeTag(tag); err != nil {
				return "", fmt.Errorf("Inbound tag %q cannot be used for a binding email: %w", tag, err)
			}
		}
		affected := 0
		for _, in := range inbounds {
			if change.Node != "all" && in["tag"] != change.Node {
				continue
			}
			tag, _ := in["tag"].(string)
			if err := validateNodeTag(tag); err != nil {
				return "", fmt.Errorf("Inbound tag %q cannot be used for a binding email: %w", tag, err)
			}
			list, _ := clientList(in)
			client := map[string]any{"id": uuid, "email": userEmailForNode(change.User, tag, perNodeEmails)}
			if flow := userFlowForInbound(in); flow != "" {
				client["flow"] = flow
			}
			if err := setClientList(in, append(list, client)); err != nil {
				return "", err
			}
			affected++
		}
		if affected == 0 {
			return "", errors.New("No inbounds available for assignment")
		}
		return fmt.Sprintf("Created user %s and assigned %d inbounds. UUID: %s", change.User, affected, uuid), nil
	case "rename":
		if !exists {
			return "", errors.New("User not found")
		}
		if !managedUserName.MatchString(change.NewName) {
			return "", errors.New("Invalid new username")
		}
		if _, ok := dir.Users[change.NewName]; ok {
			return "", errors.New("New username is already taken")
		}
		for _, in := range inbounds {
			tag, _ := in["tag"].(string)
			if err := validateNodeTag(tag); err != nil {
				return "", fmt.Errorf("Inbound tag %q cannot be used for a binding email: %w", tag, err)
			}
		}
		for _, in := range inbounds {
			tag, _ := in["tag"].(string)
			list, _ := clientList(in)
			for _, value := range list {
				c, _ := getMap(value)
				email, _ := c["email"].(string)
				if bindingOwner(email) == change.User && c["id"] == existing.ID {
					c["email"] = userEmailForNode(change.NewName, tag, perNodeEmails)
				}
			}
		}
		dir.Users[change.NewName] = existing
		delete(dir.Users, change.User)
		return "Renamed user to " + change.NewName, nil
	case "rotate":
		if !exists {
			return "", errors.New("User not found")
		}
		uuid, err := newManagedUUID()
		if err != nil {
			return "", err
		}
		for _, in := range inbounds {
			list, _ := clientList(in)
			for _, value := range list {
				c, _ := getMap(value)
				email, _ := c["email"].(string)
				if bindingOwner(email) == change.User && c["id"] == existing.ID {
					c["id"] = uuid
				}
			}
		}
		dir.Users[change.User] = directoryUser{ID: uuid}
		return "User " + change.User + " UUID rotated to " + uuid, nil
	case "delete":
		if !exists {
			return "", errors.New("User not found")
		}
		for _, in := range inbounds {
			list, _ := clientList(in)
			next := make([]any, 0, len(list))
			for _, value := range list {
				c, _ := getMap(value)
				email, _ := c["email"].(string)
				if bindingOwner(email) != change.User {
					next = append(next, value)
				}
			}
			if err := setClientList(in, next); err != nil {
				return "", err
			}
		}
		delete(dir.Users, change.User)
		return "Deleted independent user " + change.User + " and all inbound assignments", nil
	case "assign":
		if !exists {
			return "", errors.New("Create the user in user management first")
		}
		for _, in := range inbounds {
			if in["tag"] != change.Node {
				continue
			}
			tag, _ := in["tag"].(string)
			if err := validateNodeTag(tag); err != nil {
				return "", fmt.Errorf("Inbound tag %q cannot be used for a binding email: %w", tag, err)
			}
			list, _ := clientList(in)
			for _, value := range list {
				c, _ := getMap(value)
				email, _ := c["email"].(string)
				if bindingOwner(email) == change.User {
					return "", errors.New("User is already assigned to this inbound")
				}
			}
			client := map[string]any{"id": existing.ID, "email": userEmailForNode(change.User, tag, perNodeEmails)}
			if flow := userFlowForInbound(in); flow != "" {
				client["flow"] = flow
			}
			if err := setClientList(in, append(list, client)); err != nil {
				return "", err
			}
			return "Assigned " + change.User + " to inbound " + change.Node, nil
		}
		return "", errors.New("Inbound does not exist")
	case "unassign":
		if !exists {
			return "", errors.New("User not found")
		}
		for _, in := range inbounds {
			if in["tag"] != change.Node {
				continue
			}
			list, _ := clientList(in)
			next := make([]any, 0, len(list))
			found := false
			for _, value := range list {
				c, _ := getMap(value)
				email, _ := c["email"].(string)
				if bindingOwner(email) == change.User && c["id"] == existing.ID {
					found = true
				} else {
					next = append(next, value)
				}
			}
			if !found {
				return "", errors.New("User is not assigned to this inbound")
			}
			if err := setClientList(in, next); err != nil {
				return "", err
			}
			return "Unassigned " + change.User + " from " + change.Node + " (user identity retained)", nil
		}
		return "", errors.New("Inbound does not exist")
	}
	return "", errors.New("Unsupported user operation")
}

func userEmailForNode(user, tag string, perNodeEmails bool) string {
	if perNodeEmails {
		return bindingEmail(user, tag)
	}
	return user
}

func changeDirectoryUser(configPath, xrayBinary, statsServer string, change UserChange) (string, error) {
	lock, err := acquireConfigLock(configPath, 20*time.Second)
	if err != nil {
		return "", err
	}
	defer releaseConfigLock(lock)
	original, err := os.ReadFile(configPath)
	if err != nil {
		return "", err
	}
	st, err := os.Stat(configPath)
	if err != nil {
		return "", err
	}
	var root map[string]any
	if err = json.Unmarshal(original, &root); err != nil {
		return "", err
	}
	directory, oldDirectory, err := loadUserDirectory(configPath, root)
	if err != nil {
		return "", err
	}
	if change.Kind == "add" || change.Kind == "rename" || change.Kind == "assign" {
		target := change.User
		if change.Kind == "rename" {
			target = change.NewName
		}
		owners, err := nativeAccountOwners(root)
		if err != nil {
			return "", err
		}
		if owners[target] {
			return "", errors.New("A non-VLESS account uses this statistics identity; choose a distinct username or change its email first")
		}
	}
	if change.Kind == "add" || change.Kind == "rename" {
		disabled, _, err := readSuspendedStore(configPath)
		if err != nil {
			return "", err
		}
		target := change.User
		if change.Kind == "rename" {
			target = change.NewName
		}
		if _, exists := disabled.Users[target]; exists {
			return "", errors.New("A suspended user has this name; restore it or choose another name")
		}
	}
	perNodeEmails := true
	if (change.Kind == "add" && change.Node != "") || change.Kind == "assign" || change.Kind == "rename" {
		marker, markerErr := trafficMetaValue(configPath, bindingEmailsMarker)
		if markerErr != nil {
			return "", fmt.Errorf("read binding migration mode: %w", markerErr)
		}
		perNodeEmails = marker != "0"
	}
	message, err := applyDirectoryUserChange(root, &directory, change, perNodeEmails)
	if err != nil {
		return "", err
	}
	if err = ensureTrafficAmbiguitiesLocked(configPath); err != nil {
		return "", fmt.Errorf("preserving traffic ambiguity: %w", err)
	}
	nextConfig, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return "", err
	}
	nextConfig = append(nextConfig, 10)
	nextDir, err := json.MarshalIndent(directory, "", "  ")
	if err != nil {
		return "", err
	}
	nextDir = append(nextDir, 10)
	configChanged := !bytes.Equal(original, nextConfig)
	if configChanged {
		check, err := os.CreateTemp(filepath.Dir(configPath), ".freedom-user-check-*.json")
		if err != nil {
			return "", err
		}
		defer os.Remove(check.Name())
		if err = check.Chmod(st.Mode().Perm()); err != nil {
			check.Close()
			return "", err
		}
		if _, err = check.Write(nextConfig); err != nil {
			check.Close()
			return "", err
		}
		if err = check.Close(); err != nil {
			return "", err
		}
		if err = runChecked(xrayBinary, "run", "-test", "-config", check.Name()); err != nil {
			return "", fmt.Errorf("Xray rejected the proposed configuration (not committed):%w", err)
		}
	}
	path := directoryPath(configPath)
	stamp := time.Now().Format("20060102T150405.000000000")
	if err = os.WriteFile(configPath+".bot.bak."+stamp, original, 0600); err != nil {
		return "", err
	}
	if oldDirectory != nil {
		if err = os.WriteFile(path+".bak."+stamp, oldDirectory, 0600); err != nil {
			return "", err
		}
	}
	restore := func() error {
		var errs []string
		if configChanged {
			if e := writeAtomically(configPath, original, st.Mode().Perm()); e != nil {
				errs = append(errs, e.Error())
			}
		}
		if oldDirectory == nil {
			if e := os.Remove(path); e != nil && !os.IsNotExist(e) {
				errs = append(errs, e.Error())
			}
		} else if e := writeAtomically(path, oldDirectory, 0600); e != nil {
			errs = append(errs, e.Error())
		}
		if len(errs) > 0 {
			return errors.New(strings.Join(errs, "; "))
		}
		return nil
	}
	if configChanged {
		snapshotTrafficBestEffortLocked(configPath, xrayBinary, statsServer)
	}
	if change.Kind == "rename" {
		if err = copyTrafficAmbiguityLocked(configPath, change.User, change.NewName); err != nil {
			return "", fmt.Errorf("preserving traffic ambiguity: %w", err)
		}
	}
	if err = writeAtomically(path, nextDir, 0600); err != nil {
		return "", err
	}
	if configChanged {
		if err = writeAtomically(configPath, nextConfig, st.Mode().Perm()); err != nil {
			if recovery := restore(); recovery != nil {
				return "", fmt.Errorf("%v; restore failed:%v", err, recovery)
			}
			return "", err
		}
		if err = runChecked("systemctl", "restart", "xray"); err == nil {
			err = runChecked("systemctl", "is-active", "--quiet", "xray")
		}
		if err != nil {
			failure := err
			recovery := restore()
			restartErr := runChecked("systemctl", "restart", "xray")
			if recovery != nil || restartErr != nil {
				return "", fmt.Errorf("Restart failed:%v; restore files:%v; restore service:%v", failure, recovery, restartErr)
			}
			return "", fmt.Errorf("Xray failed startup verification; rolled back:%w", failure)
		}
	}
	var cleanupErrors []error
	if change.Kind == "delete" {
		if err = purgeTrafficUserLocked(configPath, change.User); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("deleting historical statistics: %w", err))
		} else if err = moveTrafficAmbiguityLocked(configPath, change.User, ""); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("clearing traffic ambiguity: %w", err))
		}
	}
	if change.Kind == "rename" {
		if err = renameTrafficStoreLocked(configPath, change.User, change.NewName); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("migrating historical statistics: %w", err))
		} else if err = moveTrafficAmbiguityLocked(configPath, change.User, change.NewName); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("migrating traffic ambiguity: %w", err))
		}
	}
	if change.Kind == "delete" || change.Kind == "rename" {
		newName := ""
		if change.Kind == "rename" {
			newName = change.NewName
		}
		if err = moveUserLimit(configPath, change.User, newName); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("updating user limits: %w", err))
		}
	}
	if len(cleanupErrors) > 0 {
		return message, fmt.Errorf("Account change succeeded, but cleanup failed: %w", errors.Join(cleanupErrors...))
	}
	return message, nil
}
