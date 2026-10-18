#!/usr/bin/env bash

#
# Send a test message via SMTP.
#
# Credentials are never stored in this script. Export them first, e.g. from
# a password manager:
#   export MAILFIRE_SMTP_USERNAME="postmaster@example.org"
#   export MAILFIRE_SMTP_PASSWORD="$(pass show mailgun/smtp)"
#

set -euo pipefail

: "${MAILFIRE_SMTP_USERNAME:?export MAILFIRE_SMTP_USERNAME first}"
: "${MAILFIRE_SMTP_PASSWORD:?export MAILFIRE_SMTP_PASSWORD first}"

cat << EOF > ./mailfire-smtp.conf
[sending]
provider = "smtp"
domain = "example.com"
from = "SMTP Test <user@httpe.org>"

[smtp]
host = "smtp.eu.mailgun.org"
port = 587
security = "starttls"
timeout = "30s"

[logging]
logfile = "./mailfire-smtp.log"
loglevel = "debug"
EOF
chmod 0600 ./mailfire-smtp.conf

./mailfire -c ./mailfire-smtp.conf --to "tk@system42.io" -m "SMTP TEST" --subject "SMTP TEST"
cat ./mailfire-smtp.log
