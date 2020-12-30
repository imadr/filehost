let $ = function(selector){
    if(selector[0] == "."){
        return document.querySelectorAll(selector);
    }
    else{
        return document.querySelector(selector);
    }
};

function prevent_default(e) {
    e.stopPropagation();
    e.preventDefault();
}

let files_queue = [];
let uploading = false;

function enqueue_files(files){
    for(let file of files){
        files_queue.push({"file": file, "name": file.name, "done": false, "progress": 0, "fromurl": 0, "error": false});
    }
    updates_files_queue();
}

function updates_files_queue(){
    $("#files").innerHTML = "";
    for(let file of files_queue){
        let file_line = document.createElement("div");
        file_line.className = "file-line";

        let size = -1;

        if(!file.fromurl){
            size = file.file.size;
        }
        else{
            size = file.size;
        }

        if(size != -1){
            let unit = 0;
            let units = ["B", "KB", "MB", "GB"];

            while(size > 1000){
                size /= 1000;
                unit++;
            }

            size = size.toFixed(2)
            file_line.innerHTML = `<span class="filename">`+file.name+`</span> `+size+` `+units[unit];
        }
        else{
            file_line.innerHTML = `<span class="filename">`+file.name+`</span>`;
        }

        let progress_bar = document.createElement("div");
        progress_bar.className = "file-progress";

        if(file.done){
            if(file.error){
                progress_bar.innerHTML = `<span class="error">`+file.progress+`</span>`;
            }
            else{
                progress_bar.innerHTML = `<a href="`+file.progress+`">`+file.progress+`</a>`;
            }
        }
        else{
            progress_bar.innerHTML = `<div class="progress"><div class="progress-value"></div></div>`;
            progress_bar.querySelector(".progress > .progress-value").style.width = file.progress+"%";
            file.progress_bar = progress_bar;
        }

        file_line.appendChild(progress_bar);
        $("#files").appendChild(file_line);
    }

    if(!uploading) upload();
}

function upload(){
    uploading = true;

    let to_upload = null;
    for(let file of files_queue){
        if(!file.done && !file.fromurl){
            to_upload = file;
            break;
        }
    }

    if(to_upload == null){
        uploading = false;
        return;
    }

    let xhr = new XMLHttpRequest();
    let form_data = new FormData();

    xhr.open("POST", "/upload", true);
    xhr.onload = function(){
        to_upload.done = true;
        to_upload.progress = xhr.response;
        to_upload.progress_bar.innerHTML = `<a href="`+to_upload.progress+`">`+to_upload.progress+`</a>`;
        upload();
    }
    xhr.onerror = function(){
        to_upload.done = true;
        to_upload.progress_bar.innerHTML = `Upload failed`;
    }
    xhr.upload.onprogress = function(e){
        to_upload.progress = e.loaded*100/e.total;
        to_upload.progress_bar.querySelector(".progress > .progress-value").style.width = to_upload.progress+"%";
    }

    form_data.append("file", to_upload.file);
    xhr.send(form_data);
}

let input = document.createElement("input");
input.type = "file";
input.multiple = true;

document.body.addEventListener("dragenter", prevent_default, false);
document.body.addEventListener("dragleave", prevent_default, false);
document.body.addEventListener("dragover", prevent_default, false);
document.body.addEventListener("drop", prevent_default, false);

document.body.addEventListener("drop", function(e){
    enqueue_files(e.dataTransfer.files);
}, false);

$("#drop").addEventListener("click", function(){
    input.click();
}, false);

$("#drop").onkeydown = function(e){
    if(e.keyCode === 13) input.click();
};

input.addEventListener("change", function(){
    enqueue_files(this.files);
});

let ws = new WebSocket("ws://"+window.location.host+"/fromurl")

$("#url-send").addEventListener("click", function(){
    if($("#url-input").checkValidity() && $("#url-input").value != ""){
        files_queue.push({"name": "Loading...", "done": false, "progress": 0, "fromurl": 1, "size": -1});
        ws.send(`{"id": `+(files_queue.length-1)+`, "url": "`+$("#url-input").value+`"}`);
        updates_files_queue();
    }
});

ws.onmessage = function(e){
    let res = JSON.parse(e.data);
    if("progress" in res){
        files_queue[res["id"]].progress = res["progress"];
    }
    else if("error" in res){
        files_queue[res["id"]].done = true;
        files_queue[res["id"]].error = true;
        files_queue[res["id"]].progress = res["error"];
        updates_files_queue();
        return
    }
    else{
        files_queue[res["id"]].done = true;
        files_queue[res["id"]].progress = res["url"];
    }
    files_queue[res["id"]].size = res["size"];
    files_queue[res["id"]].name = res["name"];
    updates_files_queue();
};