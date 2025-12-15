FROM golang:tip-alpine
WORKDIR /app
COPY . .
RUN go build -o "app" .
EXPOSE 8080
ENV PORT=8000
CMD [ "./app" ]
