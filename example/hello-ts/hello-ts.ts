/**
 * Copyright (c) 2025 AUTHORS All rights reserved.
 * Use of this source code is governed by a BSD-style
 * license that can be found in the LICENSE file.
 */

import { serve } from "https://deno.land/std/http/server.ts";

const handler = (req: Request): Response => {
  return new Response("Hello World from Deno!", {
    status: 200,
    headers: { "content-type": "text/plain" },
  });
};

console.log("Listening on http://localhost:8081");
await serve(handler, { port: 8081 })
