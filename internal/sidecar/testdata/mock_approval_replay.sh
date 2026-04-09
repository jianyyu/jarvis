#!/bin/bash
# Mock script that replays real Claude Code approval prompt bytes,
# waits for input, and reports whether it was approved.
#
# Usage: mock_approval_replay.sh <base64-encoded-prompt-bytes>
#
# The script:
# 1. Outputs the exact PTY bytes Claude Code would produce for an approval prompt
# 2. Waits for stdin input (the auto-approve keystroke)
# 3. If it receives \n within 5s, outputs "APPROVED"
# 4. Otherwise outputs "BLOCKED"

B64_FILE="$1"

if [ -z "$B64_FILE" ] || [ ! -f "$B64_FILE" ]; then
    echo "Usage: $0 <base64-file>" >&2
    exit 1
fi

# Output the raw approval prompt bytes
base64 -d "$B64_FILE"

# Small delay to let the sidecar process the output
sleep 0.3

# Wait for input with timeout
read -t 5 response
READ_EXIT=$?

if [ $READ_EXIT -eq 0 ]; then
    echo "APPROVED"
else
    echo "BLOCKED"
fi
