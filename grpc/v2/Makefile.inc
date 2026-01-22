PROTO_OPTS = paths=source_relative,M${PROTO}.proto=github.com/PlakarKorp/integration-grpc/v2/${PKGNAME}

${PROTO}.pb.go ${PROTO}_grpc.pb.go: ${PROTO}.proto
	protoc --proto_path=. \
	        -I ../ \
		--go_out=. \
		--go_opt=${PROTO_OPTS} \
		--go-grpc_out=. \
		--go-grpc_opt=${PROTO_OPTS} \
		${PROTO}.proto
