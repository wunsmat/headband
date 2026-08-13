#!/bin/sh
set -e
GOOS=windows GOARCH=amd64 go build -o headband.exe .
GOOS=linux GOARCH=amd64 go build -o headband .
echo "headband.exe / headband ready. They run:  ./headband YOUR-ADDRESS"
