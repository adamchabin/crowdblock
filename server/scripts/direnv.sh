# direnv configuration for crowdblock scripts
# This file sets up environment variables used by all scripts in the scripts/ directory.
# 
# To use: add the following to your ~/.direnvrc or enable direnv for the project:
#   source ./scripts/direnv.sh
#
# Or run: direnv allow .

# Server base URL (change if your server is hosted elsewhere)
export CROWD_BLOCK_BASE_URL="${CROWD_BLOCK_BASE_URL:-http://localhost:8080}"

# API key for authenticated requests (set this or export it in your environment)
# Get one from: POST /api/v1/register + POST /api/v1/api-keys
export CROWD_BLOCK_API_KEY="${CROWD_BLOCK_API_KEY:-}"

# Convenience: full API base URL with /api prefix
export CROWD_BLOCK_API_BASE="${CROWD_BLOCK_API_BASE:-${CROWD_BLOCK_BASE_URL}/api}"

# Optional: timeout for curl requests (seconds)
export CROWD_BLOCK_CURL_TIMEOUT="${CROWD_BLOCK_CURL_TIMEOUT:-30}"

# Validate that API key is set; warn if missing
if [[ -z "${CROWD_BLOCK_API_KEY}" ]]; then
    echo "⚠️  CROWD_BLOCK_API_KEY is not set" >&2
    echo "   Set it via: export CROWD_BLOCK_API_KEY='your-api-key'" >&2
    echo "   Or obtain one from: POST /api/v1/register + POST /api/v1/api-keys" >&2
fi