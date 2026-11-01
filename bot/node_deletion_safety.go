package main

import (
	"errors"
	"sort"
	"time"
)

var errNodeDeletionChanged = errors.New("inbound or deletion impact changed; review it again")

// Fingerprint the complete inbound and routing configuration as well as the
// sidecar-backed impact. The digest never exposes the inbound's credentials.
func nodeDeletionFingerprint(root map[string]any, report nodeDeletionReport) (string, error) {
	inbounds, ok := root["inbounds"].([]any)
	if !ok {
		return "", errors.New("missing inbounds")
	}
	var found map[string]any
	for _, value := range inbounds {
		inbound, ok := getMap(value)
		if !ok {
			return "", errors.New("invalid inbound")
		}
		if inbound["tag"] == report.Tag {
			if found != nil {
				return "", errors.New("duplicate inbound tag")
			}
			found = inbound
		}
	}
	if found == nil {
		return "", errNodeDeletionChanged
	}
	report.SuspendedUsers = append([]string(nil), report.SuspendedUsers...)
	sort.Strings(report.SuspendedUsers)
	return routingRuleETag(map[string]any{"inbound": found, "routing": root["routing"], "impact": report})
}

func inspectNodeDeletionForConfirmation(configPath, tag string) (nodeDeletionReport, string, error) {
	lock, err := acquireConfigLock(configPath, 20*time.Second)
	if err != nil {
		return nodeDeletionReport{}, "", err
	}
	defer releaseConfigLock(lock)
	report, err := inspectNodeDeletion(configPath, tag)
	if err != nil {
		return report, "", err
	}
	root, err := decodeBindingMigrationConfig(configPath)
	if err != nil {
		return report, "", err
	}
	etag, err := nodeDeletionFingerprint(root, report)
	return report, etag, err
}
