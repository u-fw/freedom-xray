# Routing API messages

`route.proto` describes the wire fields needed by Xray's public `RoutingService.TestRoute` method. Its local protobuf package is intentional: protobuf wire encoding depends on the field numbers and types. The client invokes the upstream RPC path explicitly and never registers this package as a public service.

The generated `route.pb.go` is committed so ordinary builds need no compiler. Regenerate from the `bot` directory with a stable `protoc` (verified with Debian's 3.21.12) and `protoc-gen-go` v1.36.12:

```sh
go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.12
protoc --go_out=. --go_opt=paths=source_relative internal/xrayapi/route.proto
```

Compare field numbers and types against the [upstream protocol](https://github.com/XTLS/Xray-core/blob/main/app/router/command/command.proto) and [network enum](https://github.com/XTLS/Xray-core/blob/main/common/net/network.proto) before changing this schema. Real-core tests verify interoperability; local message round trips alone are insufficient.
