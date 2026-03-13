// Copyright 2026 Merkle Works
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Client
export { X402Client } from "./client.js"

// Challenge
export {
  parseChallenge,
  canonicalize,
  sha256hex,
  hashHeaders,
  hashBody,
  CHALLENGE_HEADER,
  PROOF_HEADER,
  ACCEPT_HEADER,
} from "./challenge.js"

// Transaction
export {
  buildPartialTransaction,
  isTemplateMode,
  computeTxid,
} from "./transaction.js"

// Delegator
export { HttpDelegator } from "./delegator.js"

// Broadcaster
export { WoCBroadcaster, WOC_MAINNET, WOC_TESTNET } from "./broadcaster.js"

// Proof
export { buildProofHeader } from "./proof.js"

// Errors
export {
  X402ChallengeError,
  DelegatorError,
  BroadcastError,
  ProofRejectedError,
} from "./errors.js"

// Types
export type {
  Challenge,
  NonceRef,
  TemplateRef,
  ParsedChallenge,
  X402ClientConfig,
  DelegationRequest,
  DelegationResult,
  Delegator,
  Broadcaster,
  RequestBinding,
  Proof,
} from "./types.js"
