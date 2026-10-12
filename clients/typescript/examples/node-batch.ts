import { DUMBClient, type ClientConfig, type SubmitRequest } from "@dumb/caller";

export async function runBatch(config: ClientConfig, requests: SubmitRequest[]): Promise<string[]> {
  const client = new DUMBClient(config);
  const output: string[] = [];
  for (const request of requests) {
    const result = await client.execute(request);
    if (result.source === "dumb") output.push(result.output);
  }
  return output;
}
