// Unit tests for crowdblock/files/lib/api.uc - focusing on pure functions

// Mock required modules for the api.uc file
jest.mock('fs', () => ({}));
// Mock the config.uc module since api.uc imports from it
jest.mock('../crowdblock/files/lib/config.uc', () => ({
  debug: jest.fn(),
  ms_since: jest.fn()
}));

const { shell_quote } = 
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
