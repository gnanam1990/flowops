# Reference signer deployment

The reference signer runs inside the customer boundary. FlowOps supplies only
authorization public keys and the callback URL; it never receives the Clef
keystore, wallet key, receipt private key, raw transaction, RPC credentials, or
local journals.

## Prepare customer-owned files

1. Copy `deploy/reference-signer/config.example.json` outside the repository,
   replace every placeholder, and set mode `0600`.
2. Create a mode-`0600` freeze file:

   ```json
   {
     "version": "flowops.freeze.v1",
     "organizationFrozen": false,
     "frozenAgents": [],
     "frozenTasks": []
   }
   ```

3. Generate the separate receipt attestation key:

   ```sh
   reference-signer init-receipt-key /run/secrets/flowops-receipt-key
   ```

   Register only the printed public key with FlowOps. Back up the private file
   as customer secret material. The command will not overwrite an existing key.
4. Configure and back up Clef independently. Bind its external API to loopback,
   configure the Base chain ID, import or generate the customer EOA in Clef, and
   use Clef rules or an operator prompt to approve only the intended account and
   USDC transfer shape.
5. Put Clef and the signer in the same network namespace. A Kubernetes Pod with
   two containers is supported; separate Docker bridge containers are not,
   because the wallet endpoint is intentionally required to be loopback.

The config, freeze file, receipt key, nonce journal, and attempt journal must be
regular files inaccessible to group and other users. Symlinks fail closed. Make
the files and persistent journal directory owned by the runtime user (UID 65532
in the supplied distroless image). The journals require a persistent
customer-owned volume and one active process.

Fresh Docker named volumes are root-owned. Before the nonroot signer uses one,
an init container must set the mounted directory owner to UID/GID 65532; never
solve this by making signer secrets group- or world-readable.

## Validate and execute

```sh
reference-signer validate-config /etc/flowops/reference-signer.json
reference-signer execute /etc/flowops/reference-signer.json < signed-authorization.json
reference-signer resume /etc/flowops/reference-signer.json
```

`execute` accepts exactly one strict signed-authorization object. `resume`
never creates or signs a replacement transaction; it advances `PREPARED` once,
converts an interrupted `BROADCASTING` state to `AMBIGUOUS`, or retries only the
signed receipt callback. Output contains the authorization ID, state,
transaction hash, sender, receipt outcome, and registration state—not raw
transaction bytes or keys.

Removing the FlowOps public key from config requires restarting the one-shot
command with the updated config. Editing the freeze file takes effect on its
next check, including the last check immediately before broadcast. During any
uncertain operator incident, set `organizationFrozen` to `true` before running
another command.

## Promotion gate

Run `make smoke-reference-signer`, complete an independent security review,
then perform one explicitly approved and capped Base Sepolia transfer. Record
the authorization ID, sender, transaction hash, callback registration, and
canonical reconciliation evidence. Do not put funds in a mainnet signer from
this artifact without the remaining mainnet, legal, and security gates.
