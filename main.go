package main

import (
	"bufio"
	"bytes"
	"container/list"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"github.com/axgle/mahonia"
	"github.com/fatih/color"
	"gopkg.in/cheggaaa/pb.v1"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type CNKIArticleInfo struct {
	DownloadUrl []string `xml:"server>cluster>url"`
	DocInfo     string   `xml:"document>docInfo"`
	Filename    string   `xml:"document>filename"`
	Size        int      `xml:"document>length"`
}

type ArticleInfo struct {
	Title         string
	Issue         string
	DownloadCount int
	RefCount      int
	CreateTime    string
	Creator       []string
	SourceName    string
	SourceAlias   string
	Description   string
	ClassifyName  string
	ClassifyCode  string
}

type ArticlePropertyEntry struct {
	Name    string `json:"rdfProperty"`
	Lang    string `json:"lang"`
	ColName string `json:"colName"`
	Value   string `json:"value"`
}

type Article struct {
	Instance    string                 `json:"instance"`
	Parent      string                 `json:"rdfType"`
	Arttibutes  []ArticlePropertyEntry `json:"data"`
	Information ArticleInfo            `json:"-"`
}

type CNKISearchResult struct {
	current_result []Article
	page_size      int
	page_index     int
	page_count     int
	entries_count  int
}

type searchOption struct {
	filter  string
	databse string
	order   string
}

type cnkiSearchCache struct {
	keyword     string
	option      *searchOption
	result_list *list.List
	current     *list.Element
}

type CNKIDownloader struct {
	username     string
	password     string
	access_token string
	token_type   string
	token_expire int
	search_cache cnkiSearchCache
	http_client  *http.Client
}

type appUpdateInfo struct {
	Major       int      `json:"major"`
	Minor       int      `json:"minor"`
	ReleaseTime string   `json:"time"`
	IsAlpha     bool     `json:"is_alpha"`
	IsRequired  bool     `json:"is_required"`
	Reasons     []string `json:"reasons"`
	Url         struct {
		Windows string `json:"win"`
		Linux   string `json:"linux"`
		Mac     string `json:"mac"`
	} `json:"urls"`
}

const (
	MajorVersion         = 0
	MinorVersion         = 8
	VersionString        = "0.8-alpha"
	VersionCheckUrl      = "https://raw.githubusercontent.com/amyhaber/cnki-downloader/master/last-release.json"
	FixedDownloadViewUrl = "https://github.com/amyhaber/cnki-downloader"
	MaxDownloadThread    = 4
)

const (
	SearchBySubject = int8(1 + iota)
	SearchByAbstract
	SearchByAuthor
	SearchByKeyword
)

const (
	SearchAllDoc = int8(1 + iota)
	SearchJournal
	SearchDoctorPaper
	SearchMasterPaper
	SearchConference
)

const (
	OrderBySubject = int8(1 + iota)
	OrderByRefCount
	OrderByPublishTime
	OrderByDownloadedTime
)

var (
	searchFilterHints map[int8]string = map[int8]string{
		SearchBySubject:  "主题",
		SearchByAbstract: "摘要内容",
		SearchByAuthor:   "作者",
		SearchByKeyword:  "关键词",
	}

	searchRangeHints map[int8]string = map[int8]string{
		SearchAllDoc:      "所有库",
		SearchJournal:     "期刊",
		SearchDoctorPaper: "博士学位论文",
		SearchMasterPaper: "硕士学位论文",
		SearchConference:  "会议文献",
	}

	searchOrderHints map[int8]string = map[int8]string{
		OrderBySubject:        "主题相关度",
		OrderByRefCount:       "引用量",
		OrderByPublishTime:    "发表时间",
		OrderByDownloadedTime: "下载量",
	}

	searchFilterDefs map[int8]string = map[int8]string{
		SearchBySubject:  "dc:title",
		SearchByAbstract: "dc:description",
		SearchByAuthor:   "dc:creator",
		SearchByKeyword:  "dc:title",
	}

	searchRangeDefs map[int8]string = map[int8]string{
		SearchAllDoc:      "/data/literatures",
		SearchJournal:     "/data/journals",
		SearchDoctorPaper: "/data/doctortheses",
		SearchMasterPaper: "/data/mastertheses",
		SearchConference:  "/data/conferences",
	}

	searchOrderDefs map[int8]string = map[int8]string{
		OrderByDownloadedTime: "cnki:downloadedtime",
		OrderByRefCount:       "cnki:citedtime",
		OrderByPublishTime:    "cnki:year",
		OrderBySubject:        "dc:title",
	}
)

//
// replace all illegal chars to a underline char
//
func makeSafeFileName(fileName string) string {
	return strings.Map(func(r rune) rune {
		if strings.IndexRune(`/\:*?"><|`, r) != -1 {
			return '_'
		}
		return r
	}, fileName)
}

//
// get input string from console
//
func getInputString() string {
	buf := bufio.NewReader(os.Stdin)
	s, err := buf.ReadString('\n')
	if err != nil {
		return ""
	}

	return strings.TrimSpace(s)
}

//
// detect a document is PDF format or not
//
func isPDFDocument(fileName string) bool {
	file, err := os.Open(fileName)
	if err != nil {
		return false
	}
	defer file.Close()

	b := make([]byte, 4)
	_, err = file.Read(b)
	if err != nil {
		return false
	}

	if string(b) == "%PDF" {
		return true
	}
	return false
}

//
// input a reader(gbk), output a reader(utf-8)
//
func gbk2utf8(charset string, r io.Reader) (io.Reader, error) {
	if charset != "gb2312" {
		return nil, fmt.Errorf("Unsupported charset")
	}

	decoder := mahonia.NewDecoder("gbk")
	reader := decoder.NewReader(r)
	return reader, nil
}

//
// analyze properties and set fields
//
func (a *Article) analyze() {
	for _, attr := range a.Arttibutes {
		switch strings.ToLower(attr.Name) {
		case "dc:title":
			{
				a.Information.Title = attr.Value
			}
		case "cnki:issue":
			{
				a.Information.Issue = attr.Value
			}
		case "cnki:downloadedtime":
			{
				dc, _ := strconv.ParseInt(attr.Value, 10, 32)
				a.Information.DownloadCount = int(dc)
			}
		case "cnki:clccode":
			{
				a.Information.ClassifyName = attr.ColName
				a.Information.ClassifyCode = attr.Value
			}
		case "cnki:citedtime":
			{
				rc, _ := strconv.ParseInt(attr.Value, 10, 32)
				a.Information.RefCount = int(rc)
			}
		case "dc:creator":
			{
				a.Information.Creator = append(a.Information.Creator, attr.Value)
			}
		case "dc:source":
			{
				//
				// all
				//
				if attr.ColName == "来源代码" {
					a.Information.SourceAlias = attr.Value
				} else if attr.ColName == "来源" {
					a.Information.SourceName = attr.Value
				}

				//
				// conferences
				//
				if attr.ColName == "学会代码" {
					a.Information.SourceAlias = attr.Value
				} else if attr.ColName == "会议名称" {
					a.Information.SourceName = attr.Value
				}

				//
				// journals
				//
				if attr.ColName == "拼音刊名" {
					a.Information.SourceAlias = attr.Value
				} else if attr.ColName == "中文刊名" {
					a.Information.SourceName = attr.Value
				}

				//
				// academic
				//
				if attr.ColName == "学位授予单位" {
					a.Information.SourceName = attr.Value
				}

			}
		case "dc:date":
			{
				a.Information.CreateTime = attr.Value
			}
		case "dc:description":
			{
				a.Information.Description = attr.Value
			}
		}
	}
}

//
// get information of records
//
func (ctx *CNKISearchResult) GetRecordInfo() (count int) {
	return ctx.entries_count
}

//
// get information of page
//
func (ctx *CNKISearchResult) GetPageInfo() (size int, index int, count int) {
	return ctx.page_size, ctx.page_index, ctx.page_count
}

//
// get entries of page
//
func (ctx *CNKISearchResult) GetPageData() (entires []Article) {
	return ctx.current_result
}

//
// auth user
//
func (c *CNKIDownloader) Auth() error {
	const (
		appKey     = "2isdlw"
		appId      = "cnkimdl_clcn"
		encryptKey = `jds)(#&dsa7SDNJ32hwbds%u32j33edjdu2@**@3w`
		requestURL = "http://api.cnki.net/OAuth/OAuth/Token"
	)

	//
	// calculate params
	//
	encPassData := make([]byte, len([]byte(c.password)))
	bArray1 := []byte(c.password)
	bArray2 := []byte(encryptKey)
	for i := 0; i < len(bArray1); i++ {
		encPassData[i] = byte(uint32(bArray1[i]) ^ uint32((bArray2[i%len(bArray2)])))
	}
	encPass := base64.StdEncoding.EncodeToString(encPassData)
	encPass = encPass + "\n"

	signStamp := int64(time.Now().UnixNano() / 1000000)
	sign := strconv.FormatInt(signStamp, 10)
	enc := sha1.New()
	enc.Write([]byte(sign + appKey))
	secureKey := hex.EncodeToString(enc.Sum(nil))

	//
	// build request
	//
	param := make(url.Values)
	param.Add("grant_type", "password")
	param.Add("username", c.username)
	param.Add("password", encPass)
	param.Add("client_id", appId)
	param.Add("client_secret", secureKey)
	param.Add("sign", sign)
	//fmt.Println(param.Encode())

	req, err := http.NewRequest("POST", requestURL, strings.NewReader(param.Encode()))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Apache-HttpClient/UNAVAILABLE (java 1.4)")

	//
	// make request
	//
	resp, err := c.http_client.Do(req)
	if err != nil {
		return err
	}

	if resp.StatusCode != 200 {
		return fmt.Errorf("Response : %s", resp.Status)
	}

	//
	// parse data
	//
	result := &struct {
		Token        string `json:"access_token"`
		TokenType    string `json:"token_type"`
		Expire       int    `json:"expires_in"`
		RefreshToken string `json:"refresh_token"`
	}{}

	respData, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	err = json.Unmarshal(respData, result)
	if err != nil {
		return err
	}

	//
	// set done
	//
	c.access_token = result.Token
	c.token_expire = result.Expire
	c.token_type = result.TokenType

	return nil
}

//
// search papers
//
func (c *CNKIDownloader) Search(keyword string, option *searchOption, page int) (*CNKISearchResult, error) {
	const (
		queryDomain = "http://api.cnki.net"
		queryString = "fields=&filter=%s+eq+%s"
	)

	var (
		furl string
	)

	if page <= 0 {
		return nil, fmt.Errorf("页码无效")
	}

	//
	// build request
	//
	param := make(url.Values)

	param.Add("fields", "dc:title,cnki:issue,cnki:year,cnki:downloadedtime,dc:creator,cnki:citedtime,dc:source,dc:contributor,dc:source@py,dc:date,cnki:clccode,dc:description")
	param.Add("filter", fmt.Sprintf("%s eq %s", option.filter, keyword))
	param.Add("order", fmt.Sprintf("%s+desc", option.order))
	if page > 1 {
		param.Add("page", fmt.Sprintf("%d", page))
	}
	furl = fmt.Sprintf("%s%s?%s", queryDomain, option.databse, param.Encode())

	req, err := http.NewRequest("GET", furl, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", fmt.Sprintf("%s %s", c.token_type, c.access_token))
	req.Header.Set("User-Agent", "Apache-HttpClient/UNAVAILABLE (java 1.4)")

	//
	// do reuqest
	//
	resp, err := c.http_client.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("响应 : %s", resp.Status)
	}

	//
	// parse response data
	//

	result := &struct {
		Articles    []Article `json:"store"`
		PageSize    int       `json:"pageSize"`
		PageIndex   int       `json:"pageIndex"`
		PageCount   int       `json:"pageCount"`
		RecordCount int       `json:"recordCount"`
	}{}

	respData, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	err = json.Unmarshal(respData, result)
	if err != nil {
		return nil, err
	}

	if result.PageIndex != page {
		return nil, fmt.Errorf("查询结果(%d %d)与页码不匹配", page, result.PageIndex)
	}

	for i := 0; i < len(result.Articles); i++ {
		p := &result.Articles[i]
		p.analyze()
	}

	//
	// we done
	//
	search_context := new(CNKISearchResult)

	search_context.current_result = result.Articles
	search_context.entries_count = result.RecordCount
	search_context.page_count = result.PageCount
	search_context.page_size = result.PageSize
	search_context.page_index = result.PageIndex

	return search_context, nil
}

//
// get first page
//
func (c *CNKIDownloader) SearchFirst(keyword string, option *searchOption) (*CNKISearchResult, error) {
	s, err := c.Search(keyword, option, 1)
	if err == nil {
		c.search_cache.keyword = keyword
		c.search_cache.option = option
		c.search_cache.result_list = new(list.List)
		c.search_cache.result_list.Init()
		c.search_cache.current = c.search_cache.result_list.PushBack(s)
	}
	return s, err
}

//
// get next page
//
func (c *CNKIDownloader) SearchNext(pageNum int) (*CNKISearchResult, error) {
	if c.search_cache.result_list == nil {
		//
		// invalid context
		//
		return nil, fmt.Errorf("SearchNext方法应当在SearchFirst后调用")
	} else if c.search_cache.current == nil {
		//
		//
		//
		return nil, fmt.Errorf("未知错误")
	}

	if c.search_cache.current.Next() != nil {
		//
		// next page is present
		//
		item := c.search_cache.current.Next()
		s := item.Value.(*CNKISearchResult)

		//
		// switch
		//
		c.search_cache.current = item

		return s, nil
	} else {
		//
		// next page is invalid , we should query from server
		//
		s, err := c.Search(c.search_cache.keyword, c.search_cache.option, pageNum)
		if err == nil {
			c.search_cache.current = c.search_cache.result_list.PushBack(s)
		}
		return s, err
	}
}

//
// get previous page
//
func (c *CNKIDownloader) SearchPrev() (*CNKISearchResult, error) {
	if c.search_cache.current == nil {
		return nil, fmt.Errorf("SearchPrev方法应当在SearchNext方法后调用")
	}

	if c.search_cache.current.Prev() == nil {
		return nil, fmt.Errorf("上一页无数据")
	}

	item := c.search_cache.current.Prev()
	s := item.Value.(*CNKISearchResult)

	//
	// switch
	//
	c.search_cache.current = item

	return s, nil
}

//
// get current data
//
func (c *CNKIDownloader) CurrentPage() (*CNKISearchResult, error) {
	if c.search_cache.current == nil {
		return nil, fmt.Errorf("无搜索结果")
	}

	s := c.search_cache.current.Value.(*CNKISearchResult)
	return s, nil
}

//
// clear search context
//
func (c *CNKIDownloader) SearchStop() {
	c.search_cache.keyword = ""
	c.search_cache.current = nil
	c.search_cache.result_list = nil
	c.search_cache.option = nil
}

//
// download file
//
func (c *CNKIDownloader) getFile(url string, filename string, filesize int) error {
	var (
		success bool = false
	)

	//
	// create a file with reserved disk space
	//
	output, err := os.Create(filename)
	if err != nil {
		return err
	}

	_, err = output.Write(make([]byte, filesize))
	if err != nil {
		return err
	}

	defer func() {
		output.Close()
		if !success {
			os.Remove(filename)
		}
	}()

	//
	// prepare
	//
	furl := strings.Replace(url, "cnki://", "http://", 1)
	bar := pb.New(filesize)
	bar.SetWidth(70)
	bar.SetMaxWidth(80)
	bar.Start()

	//
	// calculate
	//
	blockSize := filesize / MaxDownloadThread
	blockRemain := filesize % MaxDownloadThread
	waitDone, syncLocker := new(sync.WaitGroup), new(sync.Mutex)

	//
	// ready for receiving error that occurred by goroutines
	//
	isErrorOccurred, occuredError := int32(0), fmt.Errorf("")

	for i := 0; i < MaxDownloadThread; i++ {

		fromOff := i * blockSize
		endOff := (i + 1) * blockSize

		if i == MaxDownloadThread-1 {
			endOff += blockRemain
		}

		waitDone.Add(1)

		//
		// download part of data with a new goroutine
		//
		go func(from, to int, file *os.File, progress *pb.ProgressBar, errorIndicator *int32, errorReceiver error, locker *sync.Mutex, waitEvent *sync.WaitGroup) {
			defer waitEvent.Done()

			//
			// new request
			//
			req, err := http.NewRequest("GET", furl, nil)
			if err != nil {
				if atomic.CompareAndSwapInt32(errorIndicator, 0, 1) {
					errorReceiver = err
				}
				return
			}

			req.Header.Set("Accept-Range", fmt.Sprintf("bytes=%d-%d", from, to))
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", from, to))
			req.Header.Set("User-Agent", "libghttp/1.0")

			//
			// do reuqest
			//
			resp, err := c.http_client.Do(req)
			if err != nil {
				if atomic.CompareAndSwapInt32(errorIndicator, 0, 1) {
					errorReceiver = err
				}
				return
			}

			//
			// check status code
			//
			if resp.StatusCode != 200 && resp.StatusCode != 206 {
				err = fmt.Errorf("在下载 (%d-%d) 时返回无效的响应码 (%d)", resp.StatusCode, from, to)
				if atomic.CompareAndSwapInt32(errorIndicator, 0, 1) {
					errorReceiver = err
				}
				return
			}

			//
			// read data
			//
			data := new(bytes.Buffer)
			data.Grow(to - from + 1)

			for {
				if *errorIndicator == 1 {
					return
				}

				n, err := io.CopyN(data, resp.Body, 4096)
				if n > 0 {
					locker.Lock()
					progress.Add(int(n))
					locker.Unlock()
				}

				if err == io.EOF {
					break
				} else if err != nil {
					if atomic.CompareAndSwapInt32(errorIndicator, 0, 1) {
						errorReceiver = err
					}
					return
				}
			}

			//
			// flush into disk
			//
			locker.Lock()
			file.WriteAt(data.Bytes(), int64(from))
			file.Sync()
			locker.Unlock()

		}(fromOff, endOff, output, bar, &isErrorOccurred, occuredError, syncLocker, waitDone)
	}

	//
	// wait all goroutines to exit
	//
	waitDone.Wait()
	bar.Finish()

	//
	// detect if there occurred some errors
	//
	if isErrorOccurred == 1 {
		return occuredError
	}

	success = true
	return nil
}

//
// get article's information
//
func (c *CNKIDownloader) getInfo(url string) (*CNKIArticleInfo, error) {
	//
	// prepare
	//
	furl := strings.Replace(url, "cnki://", "http://", 1)
	req, err := http.NewRequest("GET", furl, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Request-Action", "FileInfo")
	req.Header.Set("User-Agent", "libghttp/1.0")

	//
	// do reuqest
	//
	resp, err := c.http_client.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("响应码 : %s", resp.Status)
	}

	//
	// parse response data
	//
	// respData, err := ioutil.ReadAll(resp.Body)
	// if err != nil {
	// 	return nil, err
	// }

	result := &CNKIArticleInfo{}

	xmlDecoder := xml.NewDecoder(resp.Body)
	xmlDecoder.CharsetReader = gbk2utf8

	err = xmlDecoder.Decode(result)
	if err != nil {
		return nil, err
	}

	return result, nil
}

//
// get information url of article
//
func (c *CNKIDownloader) getInfoURL(instance string) (string, error) {
	const (
		queryURL = "http://api.cnki.net/file/%s/%s/download"
	)

	v := strings.Split(instance, ":")
	if len(v) != 2 {
		return "", fmt.Errorf("无效的 instance 字符串 %s", instance)
	}

	//
	// prepare
	//
	url := fmt.Sprintf(queryURL, v[0], v[1])
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Add("Authorization", fmt.Sprintf("%s %s", c.token_type, c.access_token))
	req.Header.Set("User-Agent", "Apache-HttpClient/UNAVAILABLE (java 1.4)")

	//
	// do reuqest
	//
	resp, err := c.http_client.Do(req)
	if err != nil {
		return "", err
	}

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("响应码 : %s", resp.Status)
	}

	//
	// parse response data
	//
	respData, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	infoURL := strings.Trim(string(respData), "\"")
	return infoURL, nil
}

//
// download paper by index
//
func (c *CNKIDownloader) Download(paper *Article) (string, error) {

	infoUrl, err := c.getInfoURL(paper.Instance)
	if err != nil {
		return "", err
	}
	fmt.Println("文档信息URL确认")

	info, err := c.getInfo(infoUrl)
	if err != nil {
		return "", err
	}
	fmt.Println("文档信息确认")

	if len(info.DownloadUrl) == 0 || len(info.Filename) == 0 {
		return "", fmt.Errorf("无效的文档信息")
	}

	currentDir, err := os.Getwd()
	if err != nil {
		return "", nil
	}
	fullName := filepath.Join(currentDir, makeSafeFileName(paper.Information.Title)+".caj")

	fmt.Printf("下载中... 共 (%d) bytes\n", info.Size)
	err = c.getFile(info.DownloadUrl[0], fullName, info.Size)
	if err != nil {
		return "", err
	}

	if isPDFDocument(fullName) {
		s := strings.Replace(fullName, filepath.Ext(fullName), ".pdf", 1)
		err = os.Rename(fullName, s)
		if err == nil {
			return s, nil
		}
	}

	return fullName, nil
}

//
// print a set of articles
//
func printArticles(page int, articles []Article) {
	fmt.Fprintf(color.Output, "\n-----------------------------------------------------------(%s)--\n", color.MagentaString("页码:%d", page))
	for id, entry := range articles {
		source := entry.Information.SourceName
		if len(source) == 0 {
			source = "N/A"
		}
		fmt.Fprintf(color.Output, "%s: %s (%s)\n",
			color.CyanString("%02d", id+1),
			color.WhiteString(entry.Information.Title),
			color.YellowString("%s", source))
	}
	fmt.Fprintf(color.Output, "-----------------------------------------------------------(%s)--\n\n", color.MagentaString("第%d页", page))
}

//
// required for serach options
//
func getSearchOpt() *searchOption {

	seletor := func(min, max, defaultValue int8, hint string, optHints map[int8]string) int8 {
		for {
			fmt.Fprintf(color.Output, "%s:\n", color.GreenString(hint))
			for k := min; k <= max; k++ {
				if k == defaultValue {

					fmt.Fprintf(color.Output, "\t %s: %s (%s)\n", color.CyanString("%d", k), optHints[k], color.GreenString("默认选项"))
				} else {
					fmt.Fprintf(color.Output, "\t %s: %s\n", color.CyanString("%d", k), optHints[k])
				}
			}

			fmt.Fprintf(color.Output, "$ %s", color.CyanString("选项: "))
			s := getInputString()
			if len(s) > 0 {
				selected, err := strconv.ParseInt(s, 16, 32)
				if err != nil || selected < int64(min) || selected > int64(max) {
					color.Red("无效的选项\n")
					continue
				}
				return int8(selected)
			}
			break
		}
		return defaultValue
	}

	// now , let the user to choose
	filter := seletor(SearchBySubject, SearchByKeyword, SearchBySubject, "请选择检索类型", searchFilterHints)
	database := seletor(SearchAllDoc, SearchConference, SearchAllDoc, "请选择检索库的范围", searchRangeHints)
	order := seletor(OrderBySubject, OrderByDownloadedTime, OrderBySubject, "请选择结果的排序依据", searchOrderHints)

	opt := &searchOption{
		filter:  searchFilterDefs[filter],
		databse: searchRangeDefs[database],
		order:   searchOrderDefs[order],
	}
	return opt
}

//
// query application update information
//
func getUpdateInfo() (*appUpdateInfo, error) {
	resp, err := http.Get(VersionCheckUrl)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("响应码 : %s", resp.Status)
	}

	//
	// parse response data
	//
	respData, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	result := &appUpdateInfo{}

	err = json.Unmarshal(respData, result)
	if err != nil {
		return nil, err
	}

	return result, nil
}

//
// try to update
//
func update() (allowContinue bool) {

	allowContinue = true
	fmt.Println("** 正在检查更新信息，当前版本: ", VersionString)

	//
	// http query the information
	//
	info, err := getUpdateInfo()
	if err == nil {
		newVersion := false
		if info.Major > MajorVersion || (info.Major == MajorVersion && info.Minor > MinorVersion) {
			newVersion = true
		}

		if newVersion {
			//
			// show version information
			//
			verName := ""
			if info.IsAlpha {
				verName = fmt.Sprintf("%d.%d-alpha", info.Major, info.Minor)
			} else {
				verName = fmt.Sprintf("%d.%d-release", info.Major, info.Minor)
			}

			fmt.Fprintf(color.Output, "\t* version: %s\n", color.GreenString(verName))
			fmt.Fprintf(color.Output, "\t*    time: %s\n", color.WhiteString(info.ReleaseTime))
			if len(info.Reasons) > 0 {
				for i, v := range info.Reasons {
					if i == 0 {
						fmt.Fprintf(color.Output, "\t*  update: - %s\n", color.WhiteString(v))
					} else {
						fmt.Fprintf(color.Output, "\t*          - %s\n", color.WhiteString(v))
					}
				}
			} else {
				fmt.Fprintf(color.Output, "\t*  update: unknown\n")
			}

			if info.IsRequired {
				fmt.Fprintf(color.Output, "** 你 %s 更新到新版本, 否则你将无法继续使用本程序\n", color.RedString("必须"))
			} else {
				fmt.Println("** 这一新版本非必要的更新，但是推荐进行更新")
			}

			fmt.Printf("** 现在进行更新? [y/n]: ")
			s := getInputString()
			if strings.ToLower(s) != "y" {
				//
				// choose to skip
				//
				allowContinue = !info.IsRequired
			} else {
				//
				// choose to update
				//
				switch runtime.GOOS {
				case "windows":
					{
						// rundll32 url.dll,FileProtocolHandler target-url
						runDll32 := filepath.Join(os.Getenv("SYSTEMROOT"), "System32", "rundll32.exe")
						cmd := exec.Command(runDll32, "url.dll,FileProtocolHandler", info.Url.Windows)
						cmd.Start()
					}
				case "darwin":
					{
						cmd := exec.Command("open", info.Url.Mac)
						cmd.Run()
					}
				case "linux":
					{
						//fmt.Fprintf(color.Output, "** url: %s \n", color.RedString(info.Url.Linux))
					}
				}
				fmt.Println("** if your browser couldn't be opened, please visit the project page to download:")
				fmt.Fprintf(color.Output, "** --> %s \n", color.GreenString(FixedDownloadViewUrl))
				allowContinue = false
			}

		} else {
			fmt.Println("** 已经是最新版本")
		}

	} else {
		fmt.Fprintf(color.Output, "** 检查 %s : %s \n", color.RedString("失败"), err.Error())
	}

	return
}

//
// lord commander
//
func main() {
	color.Cyan("******************************************************************************\n")
	color.Cyan("****  Welcome to use CNKI-Downloader, Let's fuck these knowledge mongers  ****\n")
	color.Cyan("****                            Good luck.                                ****\n")
	color.Cyan("******************************************************************************\n")

	defer func() {
		color.Yellow("** Bye.\n")
	}()

	//
	// note
	//
	fmt.Println()
	fmt.Println("** NOTE: 如果你无法下载任何文档，")
	fmt.Println("**       很可能是CNKI的服务器又炸了，")
	fmt.Println("**       请不要到GitHub上提交Issue,谢谢")
	fmt.Println("**")

	//
	// update
	//
	v := update()
	if !v {
		return
	}

	//
	// login
	//
	downloader := &CNKIDownloader{
		username:    "voidpointer",
		password:    "voidpointer",
		http_client: &http.Client{},
	}

	fmt.Printf("** 登陆中...")
	err := downloader.Auth()
	if err != nil {
		fmt.Fprintf(color.Output, "%s : %s \n", color.RedString("失败"), err.Error())
		return
	} else {
		fmt.Fprintf(color.Output, "%s\n\n", color.GreenString("成功"))
	}

	for {

		fmt.Fprintf(color.Output, "$ %s", color.CyanString("请输入欲查找的内容: "))

		s := getInputString()
		if len(s) == 0 {
			continue
		}

		//
		// search first page
		//
		opt := getSearchOpt()

		result, err := downloader.SearchFirst(s, opt)
		if err != nil {
			fmt.Fprintf(color.Output, "搜索 '%s' %s (错误码: %s)\n", s, color.RedString("失败"), err.Error())
			continue
		}
		printArticles(1, result.GetPageData())

		//
		// tips
		//
		fmt.Fprintf(color.Output, "检索到 (%s) 个项目. (请输入 '%s' 以获取帮助) \n",
			color.GreenString("%d", result.GetRecordInfo()), color.RedString("help"))

		for {
			out := false

			ctx, err := downloader.CurrentPage()
			if err != nil {
				break
			}

			psize, pindex, pcount := ctx.GetPageInfo()
			fmt.Fprintf(color.Output, "$ [%d/%d] %s", pindex, pcount, color.CyanString("command: "))

			s = getInputString()
			cmd_parts := strings.Split(s, " ")
			switch strings.ToLower(cmd_parts[0]) {
			case "help":
				{
					fmt.Fprintf(color.Output, "请使用以下命令进行操作:（不区分大小写）\n")
					fmt.Fprintf(color.Output, "\t %s: 显示当前检索页面的信息\n", color.YellowString("INFO"))
					fmt.Fprintf(color.Output, "\t %s: 转到下一页\n", color.YellowString("NEXT"))
					fmt.Fprintf(color.Output, "\t %s: 转到上一页\n", color.YellowString("PREV"))
					fmt.Fprintf(color.Output, "\t  %s: (GET ID1 ID2 ID3...), 下载本页中指定ID的文档, 例如: 可使用 GET 1 下载1号文档,GET 1 2 3 同时下载1、2、3号文档...\n", color.YellowString("GET"))
					fmt.Fprintf(color.Output, "\t %s: (SHOW ID), 现实本页中指定文档的详细信息, 例如: 可使用 SHOW 2 显示2号文档的信息...\n", color.YellowString("SHOW"))
					fmt.Fprintf(color.Output, "\t%s: 结束当前检索，开始新的检索\n", color.YellowString("BREAK"))
				}
			case "info":
				{
					color.White(" 页面条目: %d\n   页码数: %d\n 总页面数: %d\n", psize, pindex, pcount)
				}
			case "next":
				{
					next_page, err := downloader.SearchNext(pindex + 1)
					if err != nil {
						fmt.Fprintf(color.Output, "下一页不存在 (%s)\n", color.RedString(err.Error()))
					} else {
						_, index, _ := next_page.GetPageInfo()
						printArticles(index, next_page.GetPageData())
					}
				}
			case "prev":
				{
					prev_page, err := downloader.SearchPrev()
					if err != nil {
						color.Red("上一页不存在")
					} else {
						_, index, _ := prev_page.GetPageInfo()
						printArticles(index, prev_page.GetPageData())
					}
				}
			case "show":
				{

					if len(cmd_parts) < 2 {
						color.Red("输入无效")
						break
					}

					id, err := strconv.ParseInt(cmd_parts[1], 10, 32)
					if err != nil {
						fmt.Fprintf(color.Output, "输入无效 %s\n", color.RedString(err.Error()))
						break
					}
					id--

					entries := ctx.GetPageData()
					entry := entries[id]

					fmt.Println()
					fmt.Fprintf(color.Output, "*       页数: %s\n", color.WhiteString("%d", pindex))
					fmt.Fprintf(color.Output, "*         ID: %s\n", color.WhiteString("%d", id+1))
					fmt.Fprintf(color.Output, "*       标题: %s\n", color.WhiteString(entry.Information.Title))
					fmt.Fprintf(color.Output, "*   发表时间: %s\n", color.WhiteString(entry.Information.CreateTime))
					fmt.Fprintf(color.Output, "*       作者: %s\n", color.GreenString(strings.Join(entry.Information.Creator, " ")))
					fmt.Fprintf(color.Output, "*       来源: %s\n", color.GreenString("%s(%s)", entry.Information.SourceName, entry.Information.SourceAlias))
					fmt.Fprintf(color.Output, "*     分类号: %s\n", color.WhiteString("%s.%s", entry.Information.ClassifyName, entry.Information.ClassifyCode))
					fmt.Fprintf(color.Output, "*       引用: %s\n", color.RedString("%d", entry.Information.RefCount))
					fmt.Fprintf(color.Output, "*       下载: %s\n", color.WhiteString("%d", entry.Information.DownloadCount))
					fmt.Fprintf(color.Output, "*       摘要: \n")

					//text := mahonia.NewDecoder("gbk").ConvertString(entry.Information.Description)
					textSeq := []rune(entry.Information.Description)
					for j := 0; j < len(textSeq); {
						end := j + 40
						if len(textSeq)-j < 40 {
							end = len(textSeq) - 1
						}
						fmt.Printf("*%s\n", string(textSeq[j:end]))
						j = end + 1
					}
					fmt.Println()

				}
			case "get":
				{
					if len(cmd_parts) < 2 {
						color.Red("输入无效")
						break
					}

					for ii:=1;ii<len(cmd_parts);ii++ { 
						id, err := strconv.ParseInt(cmd_parts[ii], 10, 32)
						if err != nil {
							fmt.Fprintf(color.Output, "输入无效 %s\n", color.RedString(err.Error()))
							break
						}
						id--

						entries := ctx.GetPageData()

						color.White("下载中... %s\n", entries[id].Information.Title)
						path, err := downloader.Download(&entries[id])
						if err != nil {
							fmt.Fprintf(color.Output, "下载失败 %s\n", color.RedString(err.Error()))
							break
						}

						fmt.Fprintf(color.Output, "下载成功 (%s) \n", color.GreenString(path))
					}
				}
			case "break":
				{
					downloader.SearchStop()
					color.Yellow("检索结束.\n")
					out = true
				}
			}

			if out {
				break
			}
		}
	}

	return
}
