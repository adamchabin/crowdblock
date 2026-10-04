// Unit tests for crowdblock/files/lib/api.uc - focusing on pure functions

// Mock required modules for the api.uc file
jest.mock('fs', () => ({}));
jest.mock('math', () => ({}));
// Mock the config.uc module since api.uc imports from it
jest.mock('../crowdblock/files/lib/config.uc', () => ({
  debug: jest.fn(),
  ms_since: jest.fn()
}));

const { shell_quote, valid_sources, entry } = 
  require('../crowdblock/files/lib/api.uc');

describe('shell_quote', () => {
  test('quotes a simple string', () => {
    expect(shell_quote('hello')).toBe("'hello'");
  });

  test('escapes single quotes in the string', () => {
    expect(shell_quote("don't")).toBe("'don''t'");
  });

  test('handles empty string', () => {
    expect(shell_quote('')).toBe("''");
  });

  test('handles string with multiple single quotes', () => {
    expect(shell_quote("a'b'c")).toBe("'a''b''c'");
  });
});

describe('valid_sources', () => {
  test('returns null for non-array input', () => {
    expect(valid_sources(null)).toBeNull();
    expect(valid_sources({})).toBeNull();
    expect(valid_sources('string')).toBeNull();
  });

  test('returns empty array for empty array', () => {
    expect(valid_sources([])).toEqual([]);
  });

  test('filters valid sources', () => {
    const input = ['auth', 'fail2ban', 'invalid_source!', '123abc', '_underscore', 'valid-source', 'too_long_source_name_that_exceeds_thirty_two_characters'];
    const result = valid_sources(input);
    expect(result).toEqual(['auth', 'fail2ban', '123abc', 'valid-source']);
    // Note: '_underscore' is invalid because it starts with underscore
    // 'too_long_source_name_that_exceeds_thirty_two_characters' is invalid because it's > 32 chars
  });

  test('accepts sources with numbers, letters, underscores, hyphens, starting with letter or number', () => {
    const valid = ['a', 'A', '0', 'a1', '1a', 'a-b', 'a_b', 'a1-b2', 'a-b_c'];
    valid.forEach(source => {
      expect(valid_sources([source])).toEqual([source]);
    });
  });

  test('rejects sources starting with underscore or hyphen', () => {
    const invalid = ['_abc', '-abc', '_', '-'];
    invalid.forEach(source => {
      expect(valid_sources([source])).toEqual([]);
    });
  });

  test('rejects sources longer than 32 characters', () => {
    const tooLong = 'a'.repeat(33);
    expect(valid_sources([tooLong])).toEqual([]);
    const exactly32 = 'a'.repeat(32);
    expect(valid_sources([exactly32])).toEqual([exactly32]);
  });
});

describe('entry', () => {
  test('returns null for non-object input', () => {
    expect(entry(null)).toBeNull();
    expect(entry({})).toBeNull(); // empty object is still an object, but we check for required fields
    expect(entry('string')).toBeNull();
    expect(entry(123)).toBeNull();
    expect(entry([])).toBeNull();
  });

  test('returns normalized entry for valid input', () => {
    const input = {
      ip: '192.168.1.100',
      distinct_reporters: 5,
      country: 'US',
      sources: ['auth', 'fail2ban']
    };
    const result = entry(input);
    expect(result).toEqual({
      ip: '192.168.1.100',
      reports: 5,
      country: 'US',
      sources: ['auth', 'fail2ban']
    });
  });

  test('sets country to null if not a string', () => {
    const input = {
      ip: '192.168.1.100',
      distinct_reporters: 5,
      country: 123, // not a string
      sources: ['auth']
    };
    const result = entry(input);
    expect(result.country).toBeNull();
  });

  test('filters invalid sources from the sources array', () => {
    const input = {
      ip: '192.168.1.100',
      distinct_reporters: 5,
      country: 'US',
      sources: ['auth', 'invalid_source!', 'fail2ban']
    };
    const result = entry(input);
    expect(result.sources).toEqual(['auth', 'fail2ban']);
  });

  test('sets sources to empty array if all sources are invalid', () => {
    const input = {
      ip: '192.168.1.100',
      distinct_reporters: 5,
      country: 'US',
      sources: ['invalid1!', 'invalid2@']
    };
    const result = entry(input);
    expect(result.sources).toEqual([]);
  });

  test('sets sources to null if sources is not an array', () => {
    const input = {
      ip: '192.168.1.100',
      distinct_reporters: 5,
      country: 'US',
      sources: 'not an array'
    };
    const result = entry(input);
    expect(result.sources).toBeNull();
  });

  test('handles missing optional fields', () => {
    const input = {
      ip: '192.168.1.100',
      distinct_reporters: 5
      // country and sources are missing
    };
    const result = entry(input);
    expect(result).toEqual({
      ip: '192.168.1.100',
      reports: 5,
      country: null,
      sources: null
    });
  });
});