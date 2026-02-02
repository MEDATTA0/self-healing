FROM golang:alpine

RUN apk update

WORKDIR /app

COPY go.mod ./

COPY main.go ./
COPY middleware.go ./

RUN go build -o main .

EXPOSE 8080

CMD [ "./main" ]