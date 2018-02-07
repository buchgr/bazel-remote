FROM golang:latest

EXPOSE 8080

WORKDIR /go/src/app
COPY . .

RUN go-wrapper download
RUN go-wrapper install

ENTRYPOINT ["go-wrapper", "run", "--port=8080", "--dir=/data"]
CMD ["--max_size=5", "--user=${USERNAME}", "--pass=${PASSWORD}"]
