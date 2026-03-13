// Copyright 2026 Merkle Works
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

/** Thrown when the 402 challenge cannot be parsed or is invalid. */
export class X402ChallengeError extends Error {
  constructor(message: string) {
    super(message)
    this.name = "X402ChallengeError"
  }
}

/** Thrown when the delegator rejects or fails to complete the transaction. */
export class DelegatorError extends Error {
  public readonly code?: string
  public readonly status?: number

  constructor(message: string, code?: string, status?: number) {
    super(message)
    this.name = "DelegatorError"
    this.code = code
    this.status = status
  }
}

/** Thrown when the transaction cannot be broadcast to the network. */
export class BroadcastError extends Error {
  public readonly code?: string

  constructor(message: string, code?: string) {
    super(message)
    this.name = "BroadcastError"
    this.code = code
  }
}

/** Thrown when the server rejects the proof on retry. */
export class ProofRejectedError extends Error {
  public readonly status: number

  constructor(message: string, status: number) {
    super(message)
    this.name = "ProofRejectedError"
    this.status = status
  }
}
