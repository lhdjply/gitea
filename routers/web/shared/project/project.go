// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package project

import (
	"errors"

	issues_model "gitea.dev/models/issues"
	access_model "gitea.dev/models/perm/access"
	project_model "gitea.dev/models/project"
	repo_model "gitea.dev/models/repo"
	"gitea.dev/models/unit"
	"gitea.dev/modules/json"
	"gitea.dev/modules/web"
	"gitea.dev/services/context"
	"gitea.dev/services/forms"
	project_service "gitea.dev/services/projects"
)

// findProject loads the "id" path param, scoped to whichever owner the route assigned:
// anyone else's ID reads as not found. Write permission is enforced by the route.
func findProject(ctx *context.Context) *project_model.Project {
	var project *project_model.Project
	var err error
	if ctx.Repo != nil && ctx.Repo.Repository != nil {
		project, err = project_model.GetProjectForRepoByID(ctx, ctx.Repo.Repository.ID, ctx.PathParamInt64("id"))
	} else {
		project, err = project_model.GetProjectByIDAndOwner(ctx, ctx.PathParamInt64("id"), ctx.ContextUser.ID)
	}
	if err != nil {
		ctx.NotFoundOrServerError("GetProject", project_model.IsErrProjectNotExist, err)
		return nil
	}
	return project
}

func findColumn(ctx *context.Context) (*project_model.Project, *project_model.Column) {
	project := findProject(ctx)
	if ctx.Written() {
		return nil, nil
	}
	column, err := project_model.GetColumnByIDAndProjectID(ctx, ctx.PathParamInt64("columnID"), project.ID)
	if err != nil {
		ctx.NotFoundOrServerError("GetColumnByIDAndProjectID", project_model.IsErrProjectColumnNotExist, err)
		return nil, nil
	}
	return project, column
}

func MoveColumns(ctx *context.Context) {
	project := findProject(ctx)
	if ctx.Written() {
		return
	}

	type movedColumnsForm struct {
		Columns []struct {
			ColumnID int64 `json:"columnID"`
			Sorting  int64 `json:"sorting"`
		} `json:"columns"`
	}

	form := &movedColumnsForm{}
	if err := json.NewDecoder(ctx.Req.Body).Decode(&form); err != nil {
		ctx.ServerError("DecodeMovedColumnsForm", err)
		return
	}

	sortedColumnIDs := make(map[int64]int64)
	for _, column := range form.Columns {
		sortedColumnIDs[column.Sorting] = column.ColumnID
	}

	if err := project_model.MoveColumnsOnProject(ctx, project, sortedColumnIDs); err != nil {
		ctx.ServerError("MoveColumnsOnProject", err)
		return
	}

	ctx.JSONOK()
}

func AddColumnToProjectPost(ctx *context.Context) {
	form := web.GetForm[*forms.EditProjectColumnForm](ctx)
	project := findProject(ctx)
	if ctx.Written() {
		return
	}

	if err := project_model.NewColumn(ctx, &project_model.Column{
		ProjectID: project.ID,
		Title:     form.Title,
		Color:     form.Color,
		CreatorID: ctx.Doer.ID,
	}); err != nil {
		ctx.JSONErrorAuto(err)
		return
	}

	ctx.JSONOK()
}

func EditProjectColumn(ctx *context.Context) {
	form := web.GetForm[*forms.EditProjectColumnForm](ctx)
	_, column := findColumn(ctx)
	if ctx.Written() {
		return
	}

	if form.Title != "" {
		column.Title = form.Title
	}
	column.Color = form.Color
	if form.Sorting != 0 {
		column.Sorting = form.Sorting
	}

	if err := project_model.UpdateColumn(ctx, column); err != nil {
		ctx.JSONErrorAuto(err)
		return
	}

	ctx.JSONOK()
}

func DeleteProjectColumn(ctx *context.Context) {
	_, column := findColumn(ctx)
	if ctx.Written() {
		return
	}

	if err := project_model.DeleteColumnByID(ctx, column.ID); err != nil {
		ctx.ServerError("DeleteProjectColumnByID", err)
		return
	}

	ctx.JSONOK()
}

func SetDefaultProjectColumn(ctx *context.Context) {
	project, column := findColumn(ctx)
	if ctx.Written() {
		return
	}

	if err := project_model.SetDefaultColumn(ctx, project.ID, column.ID); err != nil {
		ctx.ServerError("SetDefaultColumn", err)
		return
	}

	ctx.JSONOK()
}

func MoveIssues(ctx *context.Context) {
	project, column := findColumn(ctx)
	if ctx.Written() {
		return
	}

	type movedIssuesForm struct {
		Issues []struct {
			IssueID int64 `json:"issueID"`
			Sorting int64 `json:"sorting"`
		} `json:"issues"`
	}

	form := &movedIssuesForm{}
	if err := json.NewDecoder(ctx.Req.Body).Decode(&form); err != nil {
		ctx.ServerError("DecodeMovedIssuesForm", err)
		return
	}

	issueIDs := make([]int64, 0, len(form.Issues))
	sortedIssueIDs := make(map[int64]int64)
	for _, issue := range form.Issues {
		issueIDs = append(issueIDs, issue.IssueID)
		sortedIssueIDs[issue.Sorting] = issue.IssueID
	}
	movedIssues, err := issues_model.GetIssuesByIDs(ctx, issueIDs)
	if err != nil {
		ctx.NotFoundOrServerError("GetIssueByID", issues_model.IsErrIssueNotExist, err)
		return
	}

	if len(movedIssues) != len(form.Issues) {
		ctx.ServerError("some issues do not exist", errors.New("some issues do not exist"))
		return
	}

	if _, err = movedIssues.LoadRepositories(ctx); err != nil {
		ctx.ServerError("LoadRepositories", err)
		return
	}

	for _, issue := range movedIssues {
		if !project.CanBeAccessedByOwnerRepo(issue.Repo.OwnerID, issue.Repo) {
			ctx.ServerError("Some issue's repoID is not equal to project's repoID", errors.New("Some issue's repoID is not equal to project's repoID"))
			return
		}
	}

	if err = project_service.MoveIssuesOnProjectColumn(ctx, ctx.Doer, column, sortedIssueIDs); err != nil {
		ctx.ServerError("MoveIssuesOnProjectColumn", err)
		return
	}

	ctx.JSONOK()
}

// findCardRepo returns the repository a card is added from: on a repository board the board's own
// repository, on an owner board the repository selected in the form.
func findCardRepo(ctx *context.Context, project *project_model.Project, unitType unit.Type) *repo_model.Repository {
	if ctx.Repo != nil && ctx.Repo.Repository != nil {
		return ctx.Repo.Repository
	}

	repository, err := repo_model.GetRepositoryByID(ctx, ctx.FormInt64("repo"))
	if err != nil {
		ctx.NotFoundOrServerError("GetRepositoryByID", repo_model.IsErrRepoNotExist, err)
		return nil
	}
	if repository.OwnerID != project.OwnerID {
		ctx.NotFound(errors.New("repository does not belong to the project owner"))
		return nil
	}

	perm, err := access_model.GetDoerRepoPermission(ctx, repository, ctx.Doer)
	if err != nil {
		ctx.ServerError("GetDoerRepoPermission", err)
		return nil
	}
	if !perm.CanRead(unitType) {
		ctx.NotFound(errors.New("no permission to read the repository"))
		return nil
	}
	return repository
}

func addCardToColumn(ctx *context.Context, isPull bool) {
	project := findProject(ctx)
	if ctx.Written() {
		return
	}

	column, err := project_model.GetColumnByIDAndProjectID(ctx, ctx.FormInt64("column_id"), project.ID)
	if err != nil {
		ctx.NotFoundOrServerError("GetColumnByIDAndProjectID", project_model.IsErrProjectColumnNotExist, err)
		return
	}

	unitType, number := unit.TypeIssues, ctx.FormInt64("issue_number")
	if isPull {
		unitType, number = unit.TypePullRequests, ctx.FormInt64("pull_number")
	}

	repository := findCardRepo(ctx, project, unitType)
	if ctx.Written() {
		return
	}

	issue, err := issues_model.GetIssueByIndex(ctx, repository.ID, number)
	if err != nil {
		ctx.NotFoundOrServerError("GetIssueByIndex", issues_model.IsErrIssueNotExist, err)
		return
	}
	if issue.IsPull != isPull {
		ctx.NotFound(errors.New("card type does not match the requested column"))
		return
	}

	if err := project_service.AddIssueToColumn(ctx, ctx.Doer, issue, column); err != nil {
		ctx.ServerError("AddIssueToColumn", err)
		return
	}

	if isPull {
		ctx.Flash.Success(ctx.Tr("repo.projects.column.add_pull_success", issue.Index))
	} else {
		ctx.Flash.Success(ctx.Tr("repo.projects.column.add_issue_success", issue.Index))
	}
	ctx.Redirect(project.Link(ctx))
}

func AddIssueToColumn(ctx *context.Context) {
	addCardToColumn(ctx, false)
}

func AddPullToColumn(ctx *context.Context) {
	addCardToColumn(ctx, true)
}

func UnbindIssueFromColumn(ctx *context.Context) {
	_, column := findColumn(ctx)
	if ctx.Written() {
		return
	}

	issue, err := issues_model.GetIssueByID(ctx, ctx.FormInt64("issue_id"))
	if err != nil {
		ctx.NotFoundOrServerError("GetIssueByID", issues_model.IsErrIssueNotExist, err)
		return
	}

	if err := project_service.RemoveIssueFromColumn(ctx, ctx.Doer, issue, column); err != nil {
		ctx.ServerError("RemoveIssueFromColumn", err)
		return
	}

	ctx.JSONOK()
}
