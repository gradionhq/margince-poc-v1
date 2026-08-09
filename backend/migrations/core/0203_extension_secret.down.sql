-- Reverses 0203. The mapping rows go; the sealed ciphertext they named stays
-- in vault_secret, unreferenced and inert — dropping a namespace table must
-- not be the thing that destroys secret material an operator may still need to
-- revoke at the provider that issued it.
DROP TABLE IF EXISTS extension_secret;
