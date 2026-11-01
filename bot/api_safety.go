package main

import (
	"errors"
	"net"
	"strconv"
	"strings"
)

func parseLoopbackAPIAddress(address string) (net.IP, int, error) {
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return nil, 0, errors.New("API address must be a loopback IP and port")
	}
	if strings.EqualFold(host, "localhost") {
		host = "127.0.0.1"
	}
	ip := net.ParseIP(host)
	port, err := strconv.Atoi(portText)
	if ip == nil || !ip.IsLoopback() || err != nil || port < 1 || port > 65535 {
		return nil, 0, errors.New("API address must be a loopback IP and valid port")
	}
	return ip, port, nil
}

// Adding API services must not expose them through a public direct listener,
// a broad route, a balancer, or an outbound proxy chain.
func validateAPIAccess(root map[string]any, server string) (bool, error) {
	wantedIP, wantedPort, err := parseLoopbackAPIAddress(server)
	if err != nil {
		return false, err
	}
	api, _ := getMap(root["api"])
	apiTag := networkAPITag(root)
	listen, ok := api["listen"].(string)
	if api["listen"] != nil && !ok {
		return false, errors.New("api.listen must be a loopback address")
	}
	direct := listen != ""
	if direct {
		ip, port, err := parseLoopbackAPIAddress(listen)
		if err != nil {
			return false, errors.New("api.listen must be loopback-only")
		}
		if !ip.Equal(wantedIP) || port != wantedPort {
			return false, errors.New("api.listen does not match the configured Stats API address")
		}
	}
	if inbounds, ok := root["inbounds"].([]any); ok {
		internalFound := false
		for _, value := range inbounds {
			inbound, ok := getMap(value)
			if !ok {
				return false, errors.New("invalid inbound")
			}
			if direct && inboundPortValue(inbound["port"]) == wantedPort {
				return false, errors.New("direct API listener conflicts with an inbound port")
			}
			if inbound["tag"] != "api-internal" {
				continue
			}
			if internalFound {
				return false, errors.New("duplicate api-internal inbounds")
			}
			internalFound = true
			listen, _ := inbound["listen"].(string)
			listenIP := net.ParseIP(listen)
			settings, settingsOK := getMap(inbound["settings"])
			address, _ := settings["address"].(string)
			addressIP := net.ParseIP(address)
			if listenIP == nil || !listenIP.IsLoopback() || inbound["protocol"] != "dokodemo-door" || !settingsOK || addressIP == nil || !addressIP.IsLoopback() {
				return false, errors.New("existing api-internal is not a safe loopback listener")
			}
			if !direct && (!listenIP.Equal(wantedIP) || inboundPortValue(inbound["port"]) != wantedPort) {
				return false, errors.New("api-internal does not match the configured Stats API address")
			}
		}
	} else if root["inbounds"] != nil {
		return false, errors.New("inbounds must be an array")
	}
	if routing, ok := getMap(root["routing"]); ok {
		if script, ok := routing["script"].(string); ok && script != "" {
			return false, errors.New("routing script API handling requires manual verification")
		}
		if routing["rules"] != nil {
			rules, ok := routing["rules"].([]any)
			if !ok {
				return false, errors.New("routing.rules must be an array")
			}
			for _, value := range rules {
				rule, ok := getMap(value)
				if !ok {
					return false, errors.New("invalid routing rule")
				}
				if rule["outboundTag"] == apiTag && !isInternalAPIRoute(rule, apiTag) {
					return false, errors.New("an extra routing rule can reach the internal API")
				}
			}
		}
		if routing["balancers"] != nil {
			balancers, ok := routing["balancers"].([]any)
			if !ok {
				return false, errors.New("invalid routing balancers")
			}
			for _, value := range balancers {
				balancer, ok := getMap(value)
				if !ok {
					return false, errors.New("invalid routing balancer")
				}
				selectors := stringList(balancer["selector"])
				if len(selectors) == 0 {
					return false, errors.New("invalid balancer selector")
				}
				if raw, ok := balancer["selector"].([]any); ok && len(raw) != len(selectors) {
					return false, errors.New("invalid balancer selector")
				}
				for _, selector := range selectors {
					if strings.HasPrefix(apiTag, selector) {
						return false, errors.New("a balancer can select the internal API")
					}
				}
				if balancer["fallbackTag"] == apiTag {
					return false, errors.New("a balancer can fall back to the internal API")
				}
			}
		}
	}
	if outbounds, ok := root["outbounds"].([]any); ok {
		apiOutbounds := 0
		for index, value := range outbounds {
			outbound, ok := getMap(value)
			if !ok {
				return false, errors.New("invalid outbound")
			}
			tag, _ := outbound["tag"].(string)
			if tag == apiTag {
				apiOutbounds++
				if apiOutbounds > 1 {
					return false, errors.New("duplicate API outbound tags")
				}
				if index == 0 {
					return false, errors.New("the API outbound cannot be the default")
				}
				continue
			}
			if containsNetworkReference(outbound, apiTag) {
				return false, errors.New("an outbound references the internal API")
			}
		}
	}
	return direct, nil
}
