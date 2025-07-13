const AUTH_TOKEN = Deno.env.get("CIVITAI_TOKEN");
const ITEM_PER_PAGE = 100;
const fetchPage = async (pageUrl: string) => {
  try {
    const response = await fetch(
      pageUrl,
    );
    return await response.json();
  } catch (error) {
    console.log("error", error);
  }
};

const prompts = new Set<string>();
const file = await Deno.open("prompts.txt", { write: true });
let pageUrl =
  `https://civitai.com/api/v1/images?token=${AUTH_TOKEN}&username=moutonrebelle&limit=${ITEM_PER_PAGE}`;
while (pageUrl) {
  console.log("fetching page", pageUrl);
  const data = await fetchPage(pageUrl);
  if (!data.items) console.log(data);
  const items = data.items as Array<
    {
      meta: { prompt: string };
    }
  >;
  const metadata = data.metadata as { nextPage?: string; nextCursor?: string };

  items.forEach((item) => {
    if (item.meta && item.meta.prompt) {
      prompts.add(
        item.meta.prompt.replaceAll("\n", ". ") + "\n",
      );
    }
    pageUrl = metadata.nextPage ?? "";
  });
}
prompts.forEach((prompt) => {
  Deno.writeSync(
    file.rid,
    new TextEncoder().encode(
      prompt,
    ),
  );
});
Deno.close(file.rid);
