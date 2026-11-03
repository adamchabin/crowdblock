# Unit Tests for crowdblock

This directory contains unit tests for the crowdblock OpenWrt package.
The tests are written in JavaScript using Jest and are designed to test
the pure logic functions that can be easily mocked.

## Requirements

- Node.js (version 12 or later)
- Jest

## Installation

1. Install Node.js if not already installed
2. Install Jest globally or in the project:

```bash
npm install --save-dev jest
```

## Running the Tests

From the `test` directory, run:

```bash
npx jest
```

Or if you have jest installed globally:

```bash
jest
```

## Test Files

- `config.uc.test.js` - Tests for configuration loading and parsing functions
- `ip.uc.test.js` - Tests for IP address parsing, CIDR matching, and whitelist functions
- `api.uc.test.js` - Tests for helper functions in the API module (shell quoting, validation, etc.)

## What is Tested

The tests focus on functions that are either pure or can be easily mocked:
- String parsing functions (parse_duration, parse_cidr)
- Validation functions (int_opt, dur_opt, bool_opt, valid_sources)
- Helper functions (shell_quote, entry)
- UCI loading and validation logic (with mocked UCI module)
- Whitelist building and checking

Functions that require system calls (like filesystem operations, network requests, or process execution) 
are not unit tested in this suite. Integration testing would require an OpenWrt environment or 
more complex mocking.

## Adding New Tests

To add tests for a new module:
1. Create a new test file in this directory (e.g., `newfeature.uc.test.js`)
2. Import the functions to test from the appropriate source file
3. Mock any OpenWrt-specific dependencies using `jest.mock()`
4. Write test cases using Jest's `describe` and `test` functions