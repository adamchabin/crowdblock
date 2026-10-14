// Unit tests for crowdblock/files/lib/config.uc

// Mock all OpenWrt-specific modules before requiring the uc file
jest.mock('uci', () => {
  return {
    cursor: jest.fn(() => ({
      get_all: jest.fn(),
      get: jest.fn(),
      set: jest.fn(),
      'delete': jest.fn(), // 'delete' is a reserved word, so quote it
      commit: jest.fn()
    }))
  };
});

// Mock other required modules
jest.mock('fs', () => ({}));
jest.mock('math', () => ({}));
jest.mock('uloop', () => ({}));
jest.mock('ubus', () => ({}));

const { parse_duration, int_opt, dur_opt, bool_opt, load, load_paused, save_paused, debug, ms_since } = 
  require('../crowdblock/files/lib/config.uc');

describe('parse_duration', () => {
  test('converts seconds', () => {
    expect(parse_duration('90')).toBe(90);
  });

  test('converts minutes', () => {
    expect(parse_duration('30m')).toBe(1800);
  });

  test('converts hours', () => {
    expect(parse_duration('24h')).toBe(86400);
  });

  test('converts days', () => {
    expect(parse_duration('7d')).toBe(604800);
  });

  test('returns null for invalid input', () => {
    expect(parse_duration('abc')).toBeNull();
    expect(parse_duration('12x')).toBeNull();
    expect(parse_duration('')).toBeNull();
  });
});

describe('int_opt', () => {
  test('returns default when empty or null', () => {
    expect(int_opt('', 10, 1, 100)).toBe(10);
    expect(int_opt(null, 10, 1, 100)).toBe(10);
  });

  test('returns null for non-numeric string', () => {
    expect(int_opt('abc', 10, 1, 100)).toBeNull();
  });

  test('returns null for out of range', () => {
    expect(int_opt('0', 10, 1, 100)).toBeNull();
    expect(int_opt('101', 10, 1, 100)).toBeNull();
  });

  test('returns the number when in range', () => {
    expect(int_opt('50', 10, 1, 100)).toBe(50);
    expect(int_opt('1', 10, 1, 100)).toBe(1);
    expect(int_opt('100', 10, 1, 100)).toBe(100);
  });
});

describe('dur_opt', () => {
  test('returns default when empty or null', () => {
    expect(dur_opt('', 1800, 60, 86400)).toBe(1800);
    expect(dur_opt(null, 1800, 60, 86400)).toBe(1800);
  });

  test('returns null for invalid duration', () => {
    expect(dur_opt('abc', 1800, 60, 86400)).toBeNull();
    expect(dur_opt('12x', 1800, 60, 86400)).toBeNull();
  });

  test('returns null for out of range', () => {
    expect(dur_opt('30', 1800, 60, 86400)).toBeNull(); // 30 seconds < 60 min
    expect(dur_opt('31d', 1800, 60, 86400)).toBeNull(); // 31 days > 30 days
  });

  test('returns the duration in seconds when in range', () => {
    expect(dur_opt('30m', 1800, 60, 86400)).toBe(1800);
    expect(dur_opt('1h', 1800, 60, 86400)).toBe(3600);
    expect(dur_opt('1d', 1800, 60, 86400)).toBe(86400);
  });
});

describe('bool_opt', () => {
  test('returns default when empty or null', () => {
    expect(bool_opt('', false)).toBe(false);
    expect(bool_opt(null, true)).toBe(true);
  });

  test('returns true for true values', () => {
    expect(bool_opt('1', false)).toBe(true);
    expect(bool_opt('yes', false)).toBe(true);
    expect(bool_opt('on', false)).toBe(true);
    expect(bool_opt('true', false)).toBe(true);
    expect(bool_opt('enabled', false)).toBe(true);
  });

  test('returns false for false values', () => {
    expect(bool_opt('0', true)).toBe(false);
    expect(bool_opt('no', true)).toBe(false);
    expect(bool_opt('off', true)).toBe(false);
    expect(bool_opt('false', true)).toBe(false);
    expect(bool_opt('disabled', true)).toBe(false);
  });
});

describe('load', () => {
  let cursorMock;

  beforeEach(() => {
    cursorMock = {
      get_all: jest.fn(),
      get: jest.fn(),
      set: jest.fn(),
      'delete': jest.fn(),
      commit: jest.fn()
    };
    // Mock the uci.cursor to return our mock
    require('uci').cursor.mockReturnValue(cursorMock);
  });

  test('returns error when main section is missing', () => {
    cursorMock.get_all.mockReturnValue(null);
    const result = load();
    expect(result).toEqual({ error: "missing section 'main' in /etc/config/crowdblock" });
  });

  test('returns config when all values are valid', () => {
    cursorMock.get_all.mockReturnValue({
      server: 'https://example.com',
      api_key: 'testkey',
      min_reports: '10',
      report_window: '6h',
      block_time: '24h',
      sync_interval: '10m',
      max_entries: '10000',
      timeout: '20',
      block_forward: '0',
      verify_tls: '1',
      debug: '0',
      whitelist: ['192.168.1.0/24']
    });

    const result = load();
    expect(result).toEqual({
      cfg: {
        server: 'https://example.com',
        api_key: 'testkey',
        min_reports: 10,
        report_window: 21600,
        block_time: 86400,
        sync_interval: 600,
        max_entries: 10000,
        timeout: 20,
        block_forward: false,
        verify_tls: true,
        debug: false,
        whitelist: ['192.168.1.0/24']
      }
    });
  });

  test('returns error for invalid server URL', () => {
    cursorMock.get_all.mockReturnValue({
      server: 'not a url',
      api_key: 'testkey',
      min_reports: '5',
      report_window: '6h',
      block_time: '24h',
      sync_interval: '10m',
      max_entries: '10000',
      timeout: '20',
      block_forward: '1',
      verify_tls: '1',
      debug: '0'
    });

    const result = load();
    expect(result.error).toBe('option server must be an http(s) URL');
  });

  test('returns error for missing api_key', () => {
    cursorMock.get_all.mockReturnValue({
      server: 'https://example.com',
      api_key: '',
      min_reports: '5',
      report_window: '6h',
      block_time: '24h',
      sync_interval: '10m',
      max_entries: '10000',
      timeout: '20',
      block_forward: '1',
      verify_tls: '1',
      debug: '0'
    });

    const result = load();
    expect(result.error).toBe('option api_key is required');
  });

  test('returns error for invalid min_reports', () => {
    cursorMock.get_all.mockReturnValue({
      server: 'https://example.com',
      api_key: 'testkey',
      min_reports: '3', // not in [1,5,10,20,50]
      report_window: '6h',
      block_time: '24h',
      sync_interval: '10m',
      max_entries: '10000',
      timeout: '20',
      block_forward: '1',
      verify_tls: '1',
      debug: '0'
    });

    const result = load();
    expect(result.error).toBe('option min_reports must be one of 1, 5, 10, 20, 50');
  });

  test('returns error for invalid report_window', () => {
    cursorMock.get_all.mockReturnValue({
      server: 'https://example.com',
      api_key: 'testkey',
      min_reports: '5',
      report_window: '2h', // not in [1h,6h,24h,7d]
      block_time: '24h',
      sync_interval: '10m',
      max_entries: '10000',
      timeout: '20',
      block_forward: '1',
      verify_tls: '1',
      debug: '0'
    });

    const result = load();
    expect(result.error).toBe('option report_window must be one of 1h, 6h, 24h, 7d');
  });

  test('returns error for block_time less than twice sync_interval', () => {
    cursorMock.get_all.mockReturnValue({
      server: 'https://example.com',
      api_key: 'testkey',
      min_reports: '5',
      report_window: '6h',
      block_time: '30m', // 1800 seconds
      sync_interval: '20m', // 1200 seconds -> 2*1200=2400 > 1800
      max_entries: '10000',
      timeout: '20',
      block_forward: '1',
      verify_tls: '1',
      debug: '0'
    });

    const result = load();
    expect(result.error).toBe('option block_time must be at least twice sync_interval');
  });
});

describe('load_paused', () => {
  let cursorMock;

  beforeEach(() => {
    cursorMock = {
      get: jest.fn()
    };
    require('uci').cursor.mockReturnValue(cursorMock);
  });

  test('returns empty array when paused is null', () => {
    cursorMock.get.mockReturnValue(null);
    const result = load_paused();
    expect(result).toEqual([]);
  });

  test('returns array when paused is a string', () => {
    cursorMock.get.mockReturnValue('192.168.1.1');
    const result = load_paused();
    expect(result).toEqual(['192.168.1.1']);
  });

  test('returns array when paused is an array', () => {
    cursorMock.get.mockReturnValue(['192.168.1.1', '192.168.1.2']);
    const result = load_paused();
    expect(result).toEqual(['192.168.1.1', '192.168.1.2']);
  });
});

describe('save_paused', () => {
  let cursorMock;

  beforeEach(() => {
    cursorMock = {
      set: jest.fn(),
      'delete': jest.fn(),
      commit: jest.fn()
    };
    require('uci').cursor.mockReturnValue(cursorMock);
  });

  test('sets the paused list when non-empty', () => {
    save_paused(['192.168.1.1', '192.168.1.2']);
    expect(cursorMock.set).toHaveBeenCalledWith('crowdblock', 'main', 'paused', ['192.168.1.1', '192.168.1.2']);
    expect(cursorMock['delete']).not.toHaveBeenCalled();
    expect(cursorMock.commit).toHaveBeenCalledWith('crowdblock');
  });

  test('deletes the paused list when empty', () => {
    save_paused([]);
    expect(cursorMock['delete']).toHaveBeenCalledWith('crowdblock', 'main', 'paused');
    expect(cursorMock.set).not.toHaveBeenCalled();
    expect(cursorMock.commit).toHaveBeenCalledWith('crowdblock');
  });
});

describe('debug', () => {
  test('prints when debug is true', () => {
    const consoleSpy = jest.spyOn(global, 'print');
    debug({ debug: true }, 'test %s', 'value');
    expect(consoleSpy).toHaveBeenCalledWith('DEBUG test value\\n');
    consoleSpy.mockRestore();
  });

  test('does nothing when debug is false', () => {
    const consoleSpy = jest.spyOn(global, 'print');
    debug({ debug: false }, 'test %s', 'value');
    expect(consoleSpy).not.toHaveBeenCalled();
    consoleSpy.mockRestore();
  });
});

describe('ms_since', () => {
  test('returns milliseconds since start', () => {
    // We'll mock clock to return a fixed time
    const originalClock = global.clock;
    global.clock = jest.fn(() => [10, 500000000]); // 10.5 seconds
    try {
      const start = [10, 0]; // 10.0 seconds
      expect(ms_since(start)).toBe(500); // 500 milliseconds
    } finally {
      global.clock = originalClock;
    }
  });
});