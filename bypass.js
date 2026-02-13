//npm i puppeteer-real-browser
//wget https://dl.google.com/linux/direct/google-chrome-stable_current_amd64.deb
//apt --fix-broken install
//sudo apt-get install -f
//sudo dpkg -i google-chrome-stable_current_amd64.deb
//sudo apt update && sudo apt install xvfb -y
//ubuntu setup

const { connect } = require("puppeteer-real-browser");

const TARGET = process.argv[2];
const PROXY = process.argv[3] || "";

let proxyAddress = "";
let proxyUser = null,
  proxyPass = null;

if (PROXY) {
  const proxyParts = PROXY.split(":");
  proxyAddress = `${proxyParts[0]}:${proxyParts[1]}`;
  if (proxyParts.length === 4) {
    proxyUser = proxyParts[2];
    proxyPass = proxyParts[3];
  }
}

const realBrowserOption = {
  args: proxyAddress ? [`--proxy-server=${proxyAddress}`] : [],
  turnstile: true,
  headless: false,
  customConfig: {},
  connectOption: { defaultViewport: null },
  plugins: [],
};

(async () => {
  let browser;
  const result = {
    status: "error",
    ua: "",
    cookie: "",
  };

  try {
    const { page, browser: b } = await connect(realBrowserOption);
    browser = b;

    if (proxyUser && proxyPass) {
      await page.authenticate({ username: proxyUser, password: proxyPass });
    }
    const ua = await page.evaluate(() => navigator.userAgent);
    result.ua = ua;

    if (!TARGET) {
      result.status = "success";
      result.cookie = "";
    } else {
      await page.goto(TARGET, { waitUntil: "domcontentloaded" });

      let verified = false;
      let startDate = Date.now();

      while (!verified && Date.now() - startDate < 30000) {
        const title = await page.title();
        if (title === "Attention Required! | Cloudflare") {
          result.status = "error: blocked_by_cloudflare";
          break;
        }
        if (title !== "Just a moment...") {
          verified = true;
        }
        await new Promise((r) => setTimeout(r, 1000));
      }

      if (verified) {
        const cookies = await page.cookies();
        if (cookies && cookies.length > 0) {
          const cookieString = cookies
            .map((cookie) => `${cookie.name}=${cookie.value}`)
            .join("; ");
          result.cookie = cookieString;
          result.status = "success";
        } else {
          result.status = "error: no_cookies_found";
        }
      } else if (result.status === "error") {
        result.status = "error: timeout";
      }
    }
  } catch (error) {
    result.status = `error: ${error.message}`;
  } finally {
    if (browser) {
      try {
        await browser.close();
      } catch (e) {}
    }
    console.log(JSON.stringify(result));
  }
})();
