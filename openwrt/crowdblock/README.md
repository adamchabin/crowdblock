# crowdblock – OpenWrt Client

[![License: GPL v3](https://img.shields.io/badge/License-GPLv3-blue.svg)](https://www.gnu.org/licenses/gpl-3.0)
[![OpenWrt Package](https://img.shields.io/badge/OpenWrt-Package-green.svg)](https://openwrt.org/)

Downloads and enforces a crowd-sourced IP blocklist to protect your OpenWrt router from malicious traffic.

## 📋 Overview

crowdblock is a lightweight OpenWrt package that automatically blocks IP addresses reported by other users as malicious. It maintains its own nftables table to survive firewall reloads and provides both CLI and LuCI web interface for management.

## 🔑 Key Features

- **Automatic IP Blocking**: Blocks IPs reported by sufficient independent users
- **Firewall Resilient**: Uses dedicated nftables table that survives `fw4 reload`
- **Efficient Synchronization**: Supports delta sync to minimize bandwidth usage
- **Memory Efficient**: Processes lists line-by-line to minimize RAM usage
- **Flexible Configuration**: Adjustable thresholds, timing, and whitelists
- **Comprehensive Logging**: Detailed sync information with configurable debug levels
- **LuCI Integration**: Full web interface for monitoring and management
- **Security Focused**: API key authentication with header-based fallback
- **Concurrency Safe**: File-based locking prevents sync conflicts

## 🏗️ Layout

```
Makefile                         → Package definition for OpenWrt SDK
files/crowdblock                 → /usr/sbin/crowdblock (CLI + daemon)
files/crowdblock.init            → /etc/init.d/crowdblock (procd service)
files/crowdblock.config          → /etc/config/crowdblock (UCI configuration)
files/lib/config.uc              → UCI loading and validation
files/lib/ip.uc                  → IP/CIDR parsing and whitelist logic
files/lib/api.uc                 → Server communication (uclient-fetch)
files/lib/nft.uc                 → inet crowdblock nftables management
```

## ⚙️ How It Works

1. **Initialization**: Creates dedicated `inet crowdblock` nftables table with `v4`/`v6` sets and `input`/`forward` chains at priority `filter - 10` (before fw4 rules)

2. **Periodic Synchronization**: Every `sync_interval` (±10% jitter):
   - Downloads `GET /api/v1/ips` from crowdblock server
   - Uses header-based `X-API-Key` authentication (OpenWrt 25.12+) with basic auth fallback
   - Supports gzip compression and ETag/If-None-Match for efficiency
   - Implements delta sync when possible to reduce bandwidth

3. **Processing Pipeline**:
   ```mermaid
   graph LR
   A[Download List] --> B{Validate Entries}
   B -->|Invalid/Malformed| C[Discard]
   B -->|Reports < min_reports| D[Discard]
   B -->|Whitelisted| E[Discard]
   B -->|Private/Ranges| F[Discard]
   B -->|Router's Own IPs| G[Discard]
   B -->|Valid New IP| H[Add to Blocklist]
   B -->|Existing IP| I[Refresh Expiry]
   ```

4. **Blocking Logic**:
   - Each valid IP gets `expires = now + block_time`
   - Block lasts `block_time` since last seen on list
   - Requirement: `block_time ≥ 2 × sync_interval` (prevents unblocking between syncs)
   - Both IPv4 and IPv6 sets updated atomically via single `nft -f` transaction
   - Kernel timeouts provide safety net if daemon stops

5. **State Management**:
   - Runtime state stored in `/tmp/crowdblock/state.json` (RAM-only, no flash wear)
   - Tracks: expiry times, reporter counts, countries, sources per address
   - Paused addresses tracked separately in UCI (not blocked but monitored)

6. **Error Handling**:
   - Network errors trigger exponential backoff (60s → 120s → 240s... up to sync_interval)
   - Failed syncs preserve existing blocks until kernel timeouts expire
   - Comprehensive logging available via `logread -e crowdblock`

## 🔐 API Contract

```
GET /api/v1/ips?min_reporters=5&minutes=360
Authorization: Basic base64(crowdblock:api_key)   # Legacy method
# OR
X-API-Key: api_key                               # Preferred method (header-based)
```

### Response Format (200 OK)
```json
[
  {"ip":"198.51.100.23","distinct_reporters":17,"country":"CN","sources":["auth","fail2ban"]},
  {"ip":"2001:db8::1","distinct_reporters":6},
  {"ip":"192.168.0.0/16","distinct_reporters":10}
]
```

### Response Characteristics:
- **Format**: Plain JSON array (one entry per line for memory efficiency)
- **Sorting**: Ordered by `distinct_reporters` descending (most trusted first)
- **Thresholds**: Server only serves fixed values:
  - `min_reporters`: 1, 5, 10, 20, 50
  - `report_window`: 60, 360, 1440, 10080 seconds (1h, 6h, 24h, 7d)
- **Optional Fields**:
  - `country`: ISO 3166-1 alpha-2 (when GeoIP database available)
  - `sources`: Array of detector plugins (e.g., "auth", "fail2ban")
- **ETag**: First 16 hex digits of SHA-256 of uncompressed body (quoted)
- **Delta Sync**: With `?delta=1` and `If-None-Match`, returns:
  ```json
  {
    "etag": "\"…\"",
    "count": 5000,
    "set_hash": "…",
    "remove": ["1.2.3.4"],
    "upsert": [
      {"ip":"5.6.7.8","distinct_reporters":3}
    ]
  }
  ```

## ⚠️ Important Notes

- **Security**: API key is never exposed in process lists when using header-based auth (OpenWrt 25.12+)
- **Testing**: Documentation IP ranges (`192.0.2.0/24`, `198.51.100.0/24`, etc.) are automatically whitelisted
- **Memory Usage**: ~8 MiB peak for 5,000 addresses (vs ~17 MiB if loading full list)
- **Kernel Safety**: nftables timeouts ensure blocks expire even if daemon stops unexpectedly

## 🏗️ Building

The `openwrt/` directory is an OpenWrt SDK feed - link it instead of copying:

```bash
# In OpenWrt SDK directory (run once):
echo "src-link crowdblock /path/to/crowdblock/openwrt" >> feeds.conf.default
./scripts/feeds update crowdblock
./scripts/feeds install -a -p crowdblock
make defconfig
make -j$(nproc) package/crowdblock/compile  # First build (includes dependencies)
```

For subsequent rebuilds (much faster):
```bash
./openwrt/build-crowdblock.sh          # Rebuild crowdblock package
./openwrt/build-luci-app-crowdblock.sh # Rebuild LuCI interface
```

Both scripts use `NO_DEPS=1` for speed and output the `.apk` file paths.

To install on your OpenWrt device:
```bash
apk add --allow-untrusted crowdblock-0.1.0-rN.apk luci-app-crowdblock-0.1.0-rN.apk
rm -rf /tmp/luci-*  # Refresh LuCI cache
```

## 🚀 Usage & Configuration

### Basic Setup:
```bash
# Configure server and credentials
uci set crowdblock.main.server='https://your-crowdblock-server'
uci set crowdblock.main.api_key='your_api_key_here'  # From POST /api/v1/api-keys
uci set crowdblock.main.enabled='1'
uci commit crowdblock

# Enable and start service
/etc.init.d/crowdblock enable
/etc.init.d/crowdblock start
```

### Monitoring & Management:
```bash
# Check status and statistics
crowdblock status          # Shows block counts, last sync, nft counters
crowdblock sync            # Force immediate synchronization
crowdblock sync --dry-run  # Download the list and show what would be blocked, change nothing
crowdblock pause 1.2.3.4   # Stop blocking specific IP (temporary)
crowdblock resume 1.2.3.4  # Re-enable blocking for paused IP
logread -e crowdblock      # View detailed daemon logs
nft list set inet crowdblock v4  # View currently blocked IPv4 addresses
nft list set inet crowdblock v6  # View currently blocked IPv6 addresses
```

### Advanced Configuration (via UCI):
```bash
# Adjust sensitivity (how many reports needed to block)
uci set crowdblock.main.min_reports=10

# Change how far back to look for reports
uci set crowdblock.main.report_window='6h'

# Set block duration (must be ≥ 2 × sync_interval)
uci set crowdblock.main.block_time='24h'

# Set synchronization frequency
uci set crowdblock.main.sync_interval='10m'

# Add custom whitelist entries (CIDR notation supported)
uci add crowdblock.main.whitelist='10.0.0.0/8'
uci add crowdblock.main.whitelist='192.168.1.100'
uci commit crowdblock
```

## 🛠️ Development

### Running Tests:
```bash
cd test
npm install
npm test  # Runs all unit tests
```

### Adding Functionality:
1. Modify appropriate `.uc` files in `crowdblock/files/lib/`
2. Update daemon logic in `crowdblock/files/crowdblock` as needed
3. Modify LuCI views in `luci-app-crowdblock/htdocs/luci-static/resources/`
4. Run unit tests to verify changes
5. Update documentation as needed

## 📝 TODO

- [x] **CIDR Block Support**: Enhanced server to send and client to process CIDR blocks for efficient range blocking (using nftables ipv4_prefix/ipv6_prefix sets)
- [ ] **Whitelist Optimization**: Pre-filter server download by whitelist to reduce processing overhead
- [ ] **Documentation**: Add more usage examples and troubleshooting guide

## 📄 License

This project is licensed under the GPL-3.0 License - see the [LICENSE](LICENSE) file for details.

## 🙏 Acknowledgments

- Uses [DB-IP Lite](https://db-ip.com/lite) for optional GeoIP data (CC BY 4.0)
- Inspired by various crowd-sourced blocklist projects
- Built for the OpenWrt ecosystem