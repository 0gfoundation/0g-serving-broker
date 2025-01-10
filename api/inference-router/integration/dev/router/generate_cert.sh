#!/bin/bash

CERT_FILE="certificate.pem"
KEY_FILE="privatekey.pem"

openssl genpkey -algorithm RSA -out $KEY_FILE

openssl req -new -x509 -key $KEY_FILE -out $CERT_FILE -days 365 -subj "/C=US/ST=State/L=City/O=Organization/OU=Unit/CN=example.com"
