package httpapi_test

// testPepper is API_KEY_PEPPER in tests (32 bytes, base64). It differs from
// testEncryptionKey: start-up refuses the same value for both.
const testPepper = "cGVwcGVyLXBlcHBlci1wZXBwZXItcGVwcGVyLTAxMjM="
