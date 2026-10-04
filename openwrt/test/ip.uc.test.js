// Unit tests for crowdblock/files/lib/ip.uc

// Mock required modules
jest.mock('fs', () => ({}));
jest.mock('math', () => ({}));

const { parse_cidr, cidr_contains, whitelist, whitelisted } = 
  require('../crowdblock/files/lib/ip.uc');

describe('parse_cidr', () => {
  test('parses IPv4 address without mask', () => {
    expect(parse_cidr('192.168.1.1')).toEqual({
      bytes: [192, 168, 1, 1],
      len: 32
    });
  });

  test('parses IPv4 address with mask', () => {
    expect(parse_cidr('192.168.1.0/24')).toEqual({
      bytes: [192, 168, 1, 0],
      len: 24
    });
  });

  test('parses IPv6 address without mask', () => {
    expect(parse_cidr('2001:db8::1')).toEqual({
      bytes: [0x20, 0x01, 0x0d, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1],
      len: 128
    });
  });

  test('parses IPv6 address with mask', () => {
    expect(parse_cidr('2001:db8::/32')).toEqual({
      bytes: [0x20, 0x01, 0x0d, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0],
      len: 32
    });
  });

  test('returns null for non-string input', () => {
    expect(parse_cidr(null)).toBeNull();
    expect(parse_cidr(123)).toBeNull();
    expect(parse_cidr({})).toBeNull();
  });

  test('returns null for invalid format', () => {
    expect(parse_cidr('192.168.1.1/24/extra')).toBeNull();
    expect(parse_cidr('192.168.1.1/')).toBeNull();
    expect(parse_cidr('192.168.1.1/abc')).toBeNull();
    expect(parse_cidr('192.168.1.1/33')).toBeNull(); // mask too big for IPv4
    expect(parse_cidr('2001:db8::/129')).toBeNull(); // mask too big for IPv6
  });

  test('returns null for invalid IP address', () => {
    expect(parse_cidr('999.168.1.1')).toBeNull();
    expect(parse_cidr('192.168.1.1.1')).toBeNull();
    expect(parse_cidr('2001:db8:::1')).toBeNull(); // double colon twice
  });
});

describe('cidr_contains', () => {
  test('returns true for IP in subnet', () => {
    expect(cidr_contains({ bytes: [192, 168, 1, 0], len: 24 }, [192, 168, 1, 100])).toBe(true);
    expect(cidr_contains({ bytes: [0x20, 0x01, 0x0d, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0], len: 32 }, 
                         [0x20, 0x01, 0x0d, 0xb8, 0x00, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0, 0, 0])).toBe(true);
  });

  test('returns false for IP not in subnet', () => {
    expect(cidr_contains({ bytes: [192, 168, 1, 0], len: 24 }, [192, 168, 2, 100])).toBe(false);
    expect(cidr_contains({ bytes: [0x20, 0x01, 0x0d, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0], len: 32 }, 
                         [0x20, 0x01, 0x0d, 0xb9, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0])).toBe(false);
  });

  test('returns false for mismatched byte lengths', () => {
    expect(cidr_contains({ bytes: [192, 168, 1, 0], len: 24 }, [192, 168, 1])).toBe(false);
    expect(cidr_contains({ bytes: [192, 168, 1, 0], len: 24 }, [192, 168, 1, 0, 0])).toBe(false);
  });
});

describe('whitelist', () => {
  test('returns reserved ranges when no user entries', () => {
    const result = whitelist([], (e) => { throw new Error('Should not be called'); });
    // Check that it contains at least some reserved ranges
    expect(result.length).toBeGreaterThan(0);
    // Check for a few known reserved ranges
    expect(result.some(n => 
      n.bytes[0] === 0 && n.bytes[1] === 0 && n.bytes[2] === 0 && n.bytes[3] === 0 && n.len === 8 // 0.0.0.0/8
    )).toBe(true);
    expect(result.some(n => 
      n.bytes[0] === 10 && n.len === 8 // 10.0.0.0/8
    )).toBe(true);
  });

  test('adds user entries to reserved ranges', () => {
    const userEntries = ['192.168.1.0/24', '10.0.0.0/8'];
    const onInvalid = jest.fn();
    const result = whitelist(userEntries, onInvalid);
    
    // Should have reserved ranges plus the two user entries
    expect(result.length).toBeGreaterThanOrEqual(2 + /* reserved ranges count */ 10); // at least 12
    
    // Check that the user entries are present
    expect(result.some(n => 
      n.bytes[0] === 192 && n.bytes[1] === 168 && n.bytes[2] === 1 && n.bytes[3] === 0 && n.len === 24
    )).toBe(true);
    expect(result.some(n => 
      n.bytes[0] === 10 && n.len === 8
    )).toBe(true);
    
    // onInvalid should not be called for valid entries
    expect(onInvalid).not.toHaveBeenCalled();
  });

  test('calls onInvalid for invalid entries', () => {
    const userEntries = ['192.168.1.0/24', 'invalid', '10.0.0.0/8'];
    const onInvalid = jest.fn();
    const result = whitelist(userEntries, onInvalid);
    
    // Should have reserved ranges plus the two valid user entries
    expect(result.length).toBeGreaterThanOrEqual(2 + /* reserved ranges count */ 10);
    
    // onInvalid should be called once for the invalid entry
    expect(onInvalid).toHaveBeenCalledTimes(1);
    expect(onInvalid).toHaveBeenCalledWith('invalid');
  });
});

describe('whitelisted', () => {
  test('returns true for IP in whitelist', () => {
    const nets = [
      { bytes: [0, 0, 0, 0], len: 8 }, // 0.0.0.0/8
      { bytes: [10, 0, 0, 0], len: 8 }, // 10.0.0.0/8
      { bytes: [192, 168, 1, 0], len: 24 } // 192.168.1.0/24
    ];
    
    expect(whitelisted(nets, [0, 0, 0, 1])).toBe(true); // 0.0.0.1 in 0.0.0.0/8
    expect(whitelisted(nets, [10, 0, 0, 1])).toBe(true); // 10.0.0.1 in 10.0.0.0/8
    expect(whitelisted(nets, [192, 168, 1, 100])).toBe(true); // 192.168.1.100 in 192.168.1.0/24
  });

  test('returns false for IP not in whitelist', () => {
    const nets = [
      { bytes: [0, 0, 0, 0], len: 8 }, // 0.0.0.0/8
      { bytes: [10, 0, 0, 0], len: 8 }, // 10.0.0.0/8
    ];
    
    expect(whitelisted(nets, [192, 168, 1, 1])).toBe(false);
    expect(whitelisted(nets, [172, 16, 0, 1])).toBe(false);
  });

  test('returns false for empty whitelist', () => {
    expect(whitelisted([], [192, 168, 1, 1])).toBe(false);
  });
});