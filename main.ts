// Configuration
const AUTH_TOKEN: string | undefined = Deno.env.get("CIVITAI_TOKEN");
const USERNAME: string = Deno.env.get("CIVITAI_USERNAME") || "moutonrebelle";
const ITEM_PER_PAGE = 100;

// Types
interface ImageItem {
  id: number;
  url: string;
  hash: string;
  width: number;
  height: number;
  createdAt: string;
  postId: number;
  stats: {
    cryCount: number;
    laughCount: number;
    likeCount: number;
    dislikeCount: number;
    heartCount: number;
    commentCount: number;
  };
  meta: {
    prompt?: string;
    negativePrompt?: string;
    cfgScale?: number;
    steps?: number;
    sampler?: string;
    seed?: number;
    Model?: string;
  };
  username: string;
  nsfw: boolean;
  nsfwLevel?: string; 
}

interface ApiResponse {
  items: ImageItem[];
  metadata: {
    nextPage?: string;
    nextCursor?: string;
    currentPage?: number;
    pageSize?: number;
    totalItems?: number;
    totalPages?: number;
  };
}

const fetchPage = async (pageUrl: string): Promise<ApiResponse | null> => {
  try {
    console.log(`Fetching: ${pageUrl}`);
    const response = await fetch(pageUrl, {
      headers: {
        'User-Agent': 'Civitai-Data-Fetcher/1.0'
      }
    });
    
    if (!response.ok) {
      throw new Error(`HTTP ${response.status}: ${response.statusText}`);
    }
    
    return await response.json();
  } catch (error: unknown) {
    console.error("Error fetching page:", error);
    return null;
  }
};

// Data collection
const sfwPromptPairs = new Set<string>();
const nsfwPromptPairs = new Set<string>();
const imageData: ImageItem[] = [];
let totalFetched = 0;
let downloadedCount = 0;
let skippedCount = 0;
let sfwCount = 0;
let nsfwCount = 0;

// Create images directories if they don't exist
try {
  await Deno.stat('images');
} catch {
  await Deno.mkdir('images');
  console.log('Created images directory');
}

try {
  await Deno.stat('images_nsfw');
} catch {
  await Deno.mkdir('images_nsfw');
  console.log('Created images_nsfw directory');
}

// Function to download image
const downloadImage = async (url: string, filename: string, isNsfw: boolean): Promise<boolean> => {
  try {
    const response = await fetch(url);
    if (!response.ok) {
      console.error(`Failed to download ${filename}: HTTP ${response.status}`);
      return false;
    }
    
    const arrayBuffer = await response.arrayBuffer();
    const directory = isNsfw ? 'images_nsfw' : 'images';
    await Deno.writeFile(`${directory}/${filename}`, new Uint8Array(arrayBuffer));
    return true;
  } catch (error: unknown) {
    console.error(`Error downloading ${filename}:`, error);
    return false;
  }
};

// Function to check if image already exists
// const imageExists = async (filename: string, isNsfw: boolean): Promise<boolean> => {
//   try {
//     const directory = isNsfw ? 'images_nsfw' : 'images';
//     await Deno.stat(`${directory}/${filename}`);
//     return true;
//   } catch {
//     return false;
//   }
// };

const imageExists = async (filename: string, isNsfw: boolean): Promise<boolean> => {
  try {
    if (isNsfw)
      await Deno.rename(`images/${filename}`, `images_nsfw/${filename}`);
    const directory = isNsfw ? 'images_nsfw' : 'images';
    await Deno.stat(`${directory}/${filename}`);
    return true;
  } catch {
    return false;
  }
};

// Function to get file extension from URL
const getFileExtension = (url: string): string => {
  const urlParts = url.split('.');
  const extension = urlParts[urlParts.length - 1].split('?')[0]; // Remove query params
  return ['jpg', 'jpeg', 'png', 'gif', 'webp'].includes(extension.toLowerCase()) ? extension : 'jpg';
};

// Build initial API URL (keep it simple like the original)
let pageUrl: string | null = `https://civitai.com/api/v1/images?token=${AUTH_TOKEN}&nsfw=X&username=${USERNAME}&limit=${ITEM_PER_PAGE}`;

while (pageUrl) { // Removed test limit
  const data: ApiResponse | null = await fetchPage(pageUrl);
  
  if (!data || !data.items) {
    console.error("No data received or invalid response format");
    if (data) {
      console.log("Response structure:", Object.keys(data));
    }
    break;
  }
  
  console.log(`Processing ${data.items.length} items - Next page: ${data.metadata.nextPage ? 'Yes' : 'No'}`);
  
  // Debug: Only show full metadata for first few pages
  if (totalFetched < 300) {
    console.log(`Metadata:`, JSON.stringify(data.metadata, null, 2));
  }
  for (const item of data.items) {
    totalFetched++;
    imageData.push(item);
    
    // Determine if image is NSFW
    const isNsfw = item.nsfwLevel === 'X';
    
    // Count SFW vs NSFW
    if (isNsfw) {
      nsfwCount++;
    } else {
      sfwCount++;
    }
    
    // Download image to appropriate directory
    const extension = getFileExtension(item.url);
    const filename = `${item.id}.${extension}`;
    
    const exists = await imageExists(filename, isNsfw);
    if (exists) {
      skippedCount++;
      const dir = isNsfw ? 'images_nsfw' : 'images';
      console.log(`Skipped ${filename} (already exists in ${dir})`);
    } else {
      const success = await downloadImage(item.url, filename, isNsfw);
      if (success) {
        downloadedCount++;
        const dir = isNsfw ? 'images_nsfw' : 'images';
        console.log(`Downloaded ${filename} to ${dir}`);
      }
      
      // Add small delay between downloads to be respectful
      await new Promise<void>((resolve: () => void) => setTimeout(resolve, 100));
    }
    
    // Add prompts to appropriate collection
    if (item.meta?.prompt) {
      const positivePrompt = item.meta.prompt || '';
      const negativePrompt = item.meta.negativePrompt || '';
      
      // Create prompt pair string
      const promptPair = `${positivePrompt}|||${negativePrompt}`;
      
      if (isNsfw) {
        nsfwPromptPairs.add(promptPair);
      } else {
        sfwPromptPairs.add(promptPair);
      }
    }
  }
  
  // Handle pagination
  pageUrl = data.metadata.nextPage || null;
  
  console.log(`Next page URL: ${pageUrl}`);
  console.log(`Total fetched so far: ${totalFetched}`);
  
  // Optional: Add delay to be respectful to the API
  if (pageUrl) {
    console.log(`Will fetch next page in 1 second...`);
    await new Promise<void>((resolve: () => void) => setTimeout(resolve, 1000));
  } else {
    console.log(`No more pages to fetch. Finished with ${totalFetched} images.`);
  }
}

console.log(`\nFetching complete!`);
console.log(`Total images processed: ${totalFetched}`);
console.log(`SFW images: ${sfwCount}`);
console.log(`NSFW images: ${nsfwCount}`);
console.log(`Images downloaded: ${downloadedCount}`);
console.log(`Images skipped (already exist): ${skippedCount}`);
console.log(`SFW prompt pairs found: ${sfwPromptPairs.size}`);
console.log(`NSFW prompt pairs found: ${nsfwPromptPairs.size}`);

// Load excluded words
let excludedWords: string[] = [];
try {
  const excludedWordsText = await Deno.readTextFile('excluded_words.txt');
  excludedWords = excludedWordsText
    .split(',')
    .map(word => word.trim().toLowerCase())
    .filter(word => word.length > 0);
  console.log(`Loaded ${excludedWords.length} excluded words`);
} catch (_error: unknown) {
  console.log('No excluded_words.txt found, skipping word filtering');
}

// Clean prompt function
const cleanPrompt = (prompt: string): string => {
  if (!prompt) return '';
  
  // Remove LoRA information using regex: <lora:name:weight>
  let cleaned = prompt.replace(/<lora:[^:]+:[0-9.]+>/g, '');
  
  // Remove excluded words
  if (excludedWords.length > 0) {
    const words = cleaned.split(/\s+/);
    const filteredWords: string[] = words.filter((word: string): boolean => {
      const cleanWord: string = word.toLowerCase().replace(/[^a-zA-Z0-9]/g, '');
      return !excludedWords.includes(cleanWord);
    });
    cleaned = filteredWords.join(' ');
  }
  
  // Clean up extra whitespace and newlines
  cleaned = cleaned
    .replaceAll('\n', '. ')
    .replace(/\s+/g, ' ')
    .trim();
  
  return cleaned;
};

// Process and clean SFW prompt pairs
const cleanedSfwPromptPairs: string[] = Array.from(sfwPromptPairs)
  .map((promptPair: string): string => {
    const [positivePrompt, negativePrompt]: string[] = promptPair.split('|||');
    const cleanedPositive: string = cleanPrompt(positivePrompt);
    const cleanedNegative: string = cleanPrompt(negativePrompt);
    
    // Only include pairs where at least the positive prompt exists
    if (cleanedPositive.length > 0) {
      return `${cleanedPositive}|||${cleanedNegative}`;
    }
    return '';
  })
  .filter((pair: string): boolean => pair.length > 0);

const uniqueCleanedSfwPairs: string[] = Array.from(new Set(cleanedSfwPromptPairs));

// Process and clean NSFW prompt pairs
const cleanedNsfwPromptPairs: string[] = Array.from(nsfwPromptPairs)
  .map((promptPair: string): string => {
    const [positivePrompt, negativePrompt]: string[] = promptPair.split('|||');
    const cleanedPositive: string = cleanPrompt(positivePrompt);
    const cleanedNegative: string = cleanPrompt(negativePrompt);
    
    // Only include pairs where at least the positive prompt exists
    if (cleanedPositive.length > 0) {
      return `${cleanedPositive}|||${cleanedNegative}`;
    }
    return '';
  })
  .filter((pair: string): boolean => pair.length > 0);

const uniqueCleanedNsfwPairs: string[] = Array.from(new Set(cleanedNsfwPromptPairs));

// Write output files
await Deno.writeTextFile('prompts_sfw.txt', uniqueCleanedSfwPairs.join('\n') + '\n');
await Deno.writeTextFile('prompts_nsfw.txt', uniqueCleanedNsfwPairs.join('\n') + '\n');

console.log(`SFW prompt pairs saved to prompts_sfw.txt (${uniqueCleanedSfwPairs.length} unique pairs)`);
console.log(`NSFW prompt pairs saved to prompts_nsfw.txt (${uniqueCleanedNsfwPairs.length} unique pairs)`);
