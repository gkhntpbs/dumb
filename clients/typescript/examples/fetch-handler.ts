import { DUMBClient, type ClientConfig, type ExecuteOptions, type SubmitRequest } from "@dumb/caller";

export function createHandler(config: ClientConfig, fallback: NonNullable<ExecuteOptions<string>["fallback"]>) {
  const client = new DUMBClient(config);
  return async (request: Request): Promise<Response> => {
    const input: SubmitRequest = await request.json();
    const result = await client.execute(input, { hedgeMs: 1500, fallback, signal: request.signal });
    const output = result.source === "dumb" ? result.output : result.value;
    return Response.json({ source: result.source, output });
  };
}
