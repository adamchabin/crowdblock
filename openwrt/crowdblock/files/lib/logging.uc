// Logging utilities for crowdblock

let _last_log_time = 0;
let _last_log_message = '';
let _log_rate_limit = 10; // seconds

/**
 * Rate-limited logging to prevent log spam.
 * @param {string} level - Log level (DEBUG, INFO, WARN, ERROR)
 * @param {string} fmt - Format string (like sprintf)
 * @param {...any} args - Arguments for the format string
 */
function rate_limited_log(level, fmt, ...args) {
	let now = time();
	if (now - _last_log_time < _log_rate_limit &&
	    sprintf(fmt, ...args) === _last_log_message) {
		return;
	}
	
	_last_log_time = now;
	_last_log_message = sprintf(fmt, ...args);
	
	// Format: [LEVEL] message
	print(sprintf('[%s] %s\n', level, sprintf(fmt, ...args)));
}

// Option debug: details of every sync in the system log, prefixed "DEBUG"
// (logread -e crowdblock).
function debug(cfg, fmt, ...args) {
	if (cfg?.debug)
		rate_limited_log('DEBUG', fmt, ...args);
}

// Option info: informational messages
function info(fmt, ...args) {
	rate_limited_log('INFO', fmt, ...args);
}

// Option warn: warning messages
function warn_log(fmt, ...args) {
	rate_limited_log('WARN', fmt, ...args);
}

// Option error: error messages
function error_log(fmt, ...args) {
	rate_limited_log('ERROR', fmt, ...args);
}

/**
 * Returns milliseconds since the given timestamp.
 * @param {number} then - Timestamp in seconds
 * @returns {number} Milliseconds since then
 */
function ms_since(then) {
	return (time() - then) * 1000;
}

export { debug, info, warn_log, error_log, ms_since };
