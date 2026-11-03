# luci-app-crowdblock

[![License: GPL v3](https://img.shields.io/badge/License-GPLv3-blue.svg)](https://www.gnu.org/licenses/gpl-3.0)

LuCI interface for the crowdblock OpenWrt package - accessible via **Services → Crowdblock**.

## 📋 Overview

This package provides a comprehensive web interface for managing and monitoring the crowdblock daemon through the OpenWrt LuCI framework.

## 🔑 Key Features

- **Real-time Monitoring**: View currently blocked IP addresses with details
- **Geolocation Visualization**: See blocked IPs on a map with country flags
- **Interactive Controls**: Pause/resume individual IP blocking from the web interface
- **Manual Synchronization**: Trigger immediate sync operations when needed
- **Multilingual Support**: Available in English, Polish, and German
- **Responsive Design**: Works well on both desktop and mobile devices
- **Intuitive Navigation**: Clear separation of Settings and Blocked Addresses views

## 🖥️ Interface Sections

### 🛠️ Settings
Configure the core operation of the crowdblock daemon:
- **Server Configuration**: Set the crowdblock server URL and API key
- **Sensitivity Controls**: Adjust minimum reports required and time windows
- **Timing Parameters**: Configure sync intervals and block durations
- **Network Options**: Toggle forwarded traffic blocking and TLS verification
- **Advanced Settings**: Custom whitelist entries and debug logging levels
- **Language Selection**: Choose interface language (follows LuCI by default)

### 📊 Blocked Addresses
Monitor and manage currently blocked IP addresses:
- **Live Table**: Shows all active blocks with detailed information
- **Country Flags**: Visual identification of IP origins using regional indicators
- **Search Functionality**: Filter by IP address, country code, or detection source
- **Interactive Controls**: Pause/resume individual IP blocking with one click
- **Sync Controls**: Manual synchronization button with status indicator
- **Statistical Summary**: View counts of IPv4/IPv6 addresses and paused entries
- **Last Sync Info**: Timestamp of last successful synchronization
- **Error Reporting**: Display of any synchronization issues

## 🌐 Translation Support

The interface supports multiple languages through JSON-based translation files:

```
htdocs/luci-static/resources/crowdblock/i18n/
├── en.json  ← English (default)
├── pl.json  ← Polish
└── de.json  ← German
```

### Adding a New Language

1. Copy `en.json` to `<language_code>.json` (e.g., `fr.json`)
2. Translate all values while preserving `%s`/%d` placeholders
3. Add the language code to `LANGUAGES` in `i18n.js`
4. Run the validation script to check for completeness

```bash
# Validate translation files
./check-i18n.py

# Update English template and synchronize translations
./check-i18n.py --update
```

## 🔧 Technical Implementation

- **Lightweight Design**: Uses uci, fs, and json modules efficiently
- **Real-time Updates**: Automatic polling every 10 seconds for fresh data
- **Efficient Rendering**: Optimized table updates for large blocklists
- **Secure Integration**: Proper ACL enforcement for access control
- **Standard LuCI Patterns**: Follows established OpenWrt UI conventions

## 📝 Usage

After installing both `crowdblock` and `luci-app-crowdblock` packages:

1. Access via LuCI menu: **Services → Crowdblock**
2. Configure server and API key in the **Settings** tab
3. Enable the service and save & apply changes
4. Monitor blocked addresses in the **Blocked addresses** tab
5. Use pause/resume controls for individual IP management
6. Trigger manual sync when immediate updates are needed

## 📄 License

This project is licensed under the GPL-3.0 License - see the [LICENSE](LICENSE) file for details.