PROTO_OPTS = paths=source_relative,M${PROTO}.proto=github.com/PlakarKorp/integration-grpc/${PROTO}

pkg/${PROTO}.pb.go pkg/${PROTO}_grpc.pb.go: ${PROTO}.proto
	protoc --proto_path=. \
		--go_out=./pkg \
		--go_opt=${PROTO_OPTS} \
		--go-grpc_out=./pkg \
		--go-grpc_opt=${PROTO_OPTS} \
		${PROTO}.proto
