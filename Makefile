VERSION=$(shell bin/glauth64 --version)

run:
	go run glauth.go bindata.go ldapbackend.go webapi.go configbackend.go -c glauth.cfg

all: prep binaries docker

prep:
	export GOPATH=$(PWD)/packages/
	mkdir -p bin
	go get -u github.com/jteeuwen/go-bindata/...

binaries: bindata linux32 linux64 darwin64

bindata: prep
	${GOPATH}/bin/go-bindata -pkg=main assets

linux32: bindata
	GOOS=linux GOARCH=386 go build -o bin/glauth32 glauth.go bindata.go ldapbackend.go webapi.go configbackend.go

linux64: bindata
	GOOS=linux GOARCH=amd64 go build -o bin/glauth64 glauth.go bindata.go ldapbackend.go webapi.go configbackend.go

darwin64: bindata
	GOOS=darwin GOARCH=amd64 go build -o bin/glauthOSX glauth.go bindata.go ldapbackend.go webapi.go configbackend.go

docker: certs
	docker build -t pasientskyhosting/glauth .

docker-run:
	docker run -p 5555:5555 -p 636:663 -p 389:389 pasientskyhosting/glauth

certs:
	rm -rf certs
	mkdir -p certs
	openssl genrsa -out certs/server.key 2048
	openssl req -new -key certs/server.key -out certs/server.csr -subj "/CN=glauth"
	openssl x509 -req -days 365 -in certs/server.csr -signkey certs/server.key -out certs/server.crt
